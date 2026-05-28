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
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/admission"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
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
	basePath        string
	admitter        admission.Admitter
	resolver        Resolver
	freeze          FreezeHandoff
	heat            HeatToucher
	logger          logging.Logger
	httpc           *http.Client
	cache           *resolveCache
	blockSize       int64
	coldFetch       *blockFetchCoalescer
	nodeID          string
	relayAuthSecret []byte
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
	// NodeID is this Helmsman's own node id. The cross-cluster
	// peer-relay middleware accepts requests only when the inbound
	// JWT's audience claim matches this value. Required when
	// RelayAuthSecret is set; otherwise non-localhost requests are
	// rejected unconditionally.
	NodeID string
	// RelayAuthSecret is the shared HMAC secret used to validate
	// inbound artifact_relay JWTs from peer edges (origin Foghorn
	// signs with the same key). Empty disables non-localhost access
	// entirely.
	RelayAuthSecret []byte
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
	return &Server{
		basePath:        opts.BasePath,
		admitter:        opts.Admitter,
		resolver:        opts.Resolver,
		freeze:          opts.Freeze,
		heat:            opts.Heat,
		logger:          opts.Logger,
		httpc:           c,
		cache:           newResolveCache(),
		blockSize:       blockSize,
		coldFetch:       newBlockFetchCoalescer(),
		nodeID:          strings.TrimSpace(opts.NodeID),
		relayAuthSecret: append([]byte(nil), opts.RelayAuthSecret...),
	}
}

// MountRoutes registers the /internal/artifact/* route group on the given
// Gin engine. Localhost requests (Mist on the same box) bypass auth so
// the normal cold-fetch path is unchanged. Non-localhost requests must
// carry a valid artifact_relay JWT (Authorization: Bearer) signed by
// the local Foghorn — these come from peer edges (same or other
// cluster) that need to read hot-but-unsynced bytes from this node.
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
	// emits this shape — output_stream_name is required on
	// ClipPullRequest, so the wildcard always carries the
	// stream/hash split. Stream identity in the path (rather than
	// ?s=) survives Mist's input + ".dtsh" sidecar mutation.
	g.HEAD("/clip/*path", func(c *gin.Context) { s.serveClipRoute(c) })
	g.GET("/clip/*path", func(c *gin.Context) { s.serveClipRoute(c) })
	g.PUT("/clip/*path", func(c *gin.Context) { s.putClipRoute(c) })

	// Processing input. Same flat layout under storage/upload/.
	g.HEAD("/upload/:file", s.serveUpload)
	g.GET("/upload/:file", s.serveUpload)
	g.PUT("/upload/:file", func(c *gin.Context) { s.putSidecar(c, "upload") })
	g.PUT("/upload/:file/", func(c *gin.Context) { s.putSidecar(c, "upload") })
}

// peerAuthMiddleware returns the JWT gate for /internal/artifact/*.
// Loopback callers (RemoteAddr 127.0.0.1 / ::1) pass through unchanged
// ONLY when they carry no proxy-forwarding markers — Mist on the same
// box never carries those; Caddy reverse-proxying remote traffic to
// loopback always does. Anything else needs a valid artifact_relay JWT
// with aud=own-node-id and path=request-path. Missing/invalid → 401.
//
// The proxy-marker check is the critical second leg: Caddy's
// reverse_proxy hop arrives at this listener over 127.0.0.1, so a
// pure RemoteAddr check would let any external request through the
// edge FQDN bypass the JWT. Caddy sets X-Forwarded-For / -Proto /
// -Host on every proxied request; the presence of ANY of these on a
// loopback caller means the traffic originated remotely.
func (s *Server) peerAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isLoopbackRemoteAddr(c.Request.RemoteAddr) && !hasProxyForwardMarker(c.Request.Header) {
			c.Next()
			return
		}
		if len(s.relayAuthSecret) == 0 || s.nodeID == "" {
			// No secret configured: only loopback is allowed. This is
			// the safe failure mode — silently deny non-loopback rather
			// than open the relay because secrets weren't wired.
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		authz := c.Request.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(authz, "Bearer ")
		artifactHash := artifactHashFromPath(c.Request.URL.Path)
		if artifactHash == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		_, err := auth.ValidateArtifactRelayJWT(token, s.relayAuthSecret, s.nodeID, artifactHash, c.Request.URL.Path)
		if err != nil {
			if s.logger != nil {
				s.logger.WithError(err).WithField("remote_addr", c.Request.RemoteAddr).Debug("artifact relay token rejected")
			}
			// Signal expired tokens via WWW-Authenticate so the peer
			// can re-resolve without parsing a body.
			if errors.Is(err, auth.ErrExpiredArtifactRelay) {
				c.Writer.Header().Set("WWW-Authenticate", `Bearer error="token_expired"`)
			}
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
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

// artifactHashFromPath extracts the artifact hash from a request like
//
//	/internal/artifact/vod/<hash>.<ext>
//	/internal/artifact/clip/<stream>/<hash>.<ext>
//	/internal/artifact/upload/<hash>.<ext>
//
// Empty when the path doesn't end in <hash>.<ext>. The hash itself is
// the basename minus the extension.
func artifactHashFromPath(path string) string {
	base := filepath.Base(path)
	if base == "" || base == "." || base == "/" {
		return ""
	}
	ext := filepath.Ext(base)
	if ext == "" {
		return base
	}
	return strings.TrimSuffix(base, ext)
}
