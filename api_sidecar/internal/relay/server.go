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
	"net/http"

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
	basePath  string
	admitter  admission.Admitter
	resolver  Resolver
	freeze    FreezeHandoff
	heat      HeatToucher
	logger    logging.Logger
	httpc     *http.Client
	cache     *resolveCache
	blockSize int64
	coldFetch *blockFetchCoalescer
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
		basePath:  opts.BasePath,
		admitter:  opts.Admitter,
		resolver:  opts.Resolver,
		freeze:    opts.Freeze,
		heat:      opts.Heat,
		logger:    opts.Logger,
		httpc:     c,
		cache:     newResolveCache(),
		blockSize: blockSize,
		coldFetch: newBlockFetchCoalescer(),
	}
}

// MountRoutes registers the /internal/artifact/* route group on the given
// Gin engine. Routes are reachable only via Helmsman's existing listener
// (already internal/local-only); no per-route auth is added.
func (s *Server) MountRoutes(r *gin.Engine) {
	g := r.Group("/internal/artifact")

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
