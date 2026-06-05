package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetrytoken"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Per-IP rate limit for the public, unauthenticated beacons. Telemetry is lossy
// by design — over-limit requests are dropped (204), never queued.
const (
	beaconRateLimit = 120 // requests/min per IP (one-shot beacons, e.g. boot)
	beaconBurst     = 60

	// Session beacons recur (heartbeats ~2/min + visibility/final), so a flat
	// per-IP limit starves co-tenant viewers behind one NAT. Instead each viewer
	// session gets its own budget, with a generous per-IP backstop that still
	// bounds a client flooding by rotating session ids.
	beaconSessionLimit   = 20 // requests/min per (IP, session)
	beaconSessionBurst   = 10
	beaconSessionIPLimit = 3000 // requests/min per IP backstop (large-NAT headroom)
	beaconSessionIPBurst = 600
)

// rateLimiter is the minimal slice of middleware.RateLimiter the beacons need,
// declared locally to avoid coupling handlers to the middleware package.
type rateLimiter interface {
	Allow(key string, limit, burst int) (allowed bool, remaining int, resetSeconds int)
}

// playbackContentResolver is the slice of the Commodore client used to map a
// public content_id to its owning tenant/stream/artifact.
type playbackContentResolver interface {
	ResolveArtifactPlaybackID(ctx context.Context, playbackID string) (*commodorepb.ResolveArtifactPlaybackIDResponse, error)
	ResolvePlaybackID(ctx context.Context, playbackID string) (*commodorepb.ResolvePlaybackIDResponse, error)
}

// triggerSink is the slice of the Decklog client used to forward a trigger.
type triggerSink interface {
	SendTriggerContext(ctx context.Context, trigger *ipcpb.MistTrigger) error
}

// beaconAttribution is the server-derived ownership of a content_id. The browser
// is untrusted; every field here comes from Commodore, never the beacon body.
type beaconAttribution struct {
	tenantID        string
	streamID        string
	artifactHash    string
	internalName    string
	originClusterID string
	contentType     string
}

// BeaconIntake holds the shared, content-agnostic plumbing for public player
// telemetry beacons: per-IP rate limiting, server-side content attribution, and
// optional telemetry-token cluster attribution. Boot and session beacons both
// build on it so the trust boundary lives in exactly one place.
type BeaconIntake struct {
	commodore       playbackContentResolver
	limiter         rateLimiter
	attrCache       *cache.Cache
	telemetrySecret []byte
	logger          logging.Logger
}

func NewBeaconIntake(
	commodore playbackContentResolver,
	limiter rateLimiter,
	telemetrySecret []byte,
	logger logging.Logger,
) *BeaconIntake {
	// content_id -> attribution. Bridge can't key the Commodore client's own
	// per-tenant cache pre-resolution, so a small content-id cache absorbs many
	// viewers of the same stream and is shared across all beacon types.
	attrCache := cache.New(cache.Options{
		TTL:         60 * time.Second,
		NegativeTTL: 30 * time.Second,
		MaxEntries:  10000,
	}, cache.MetricsHooks{})
	return &BeaconIntake{
		commodore:       commodore,
		limiter:         limiter,
		attrCache:       attrCache,
		telemetrySecret: telemetrySecret,
		logger:          logger,
	}
}

// rateLimited applies the default per-IP limit and writes 204 when the beacon is
// dropped. Returns true if the caller should stop (request was rejected).
func (b *BeaconIntake) rateLimited(c *gin.Context, keyPrefix string) bool {
	return b.rateLimitedKey(c, keyPrefix+c.ClientIP(), beaconRateLimit, beaconBurst)
}

// rateLimitedKey applies an explicit limit/burst against an explicit key and
// writes 204 when the beacon is dropped. Returns true if the caller should stop.
func (b *BeaconIntake) rateLimitedKey(c *gin.Context, key string, limit, burst int) bool {
	if b.limiter == nil {
		return false
	}
	if allowed, _, _ := b.limiter.Allow(key, limit, burst); !allowed {
		c.Status(http.StatusNoContent)
		return true
	}
	return false
}

// resolveAttribution maps a public content_id to its owning tenant/stream/artifact
// via Commodore (artifact first, then live stream), cached by content_id. The
// cache key is beacon-type-agnostic so boot and session beacons share resolutions.
func (b *BeaconIntake) resolveAttribution(ctx context.Context, contentID string) (beaconAttribution, bool) {
	val, found, err := b.attrCache.Get(ctx, "attr:"+contentID, func(ctx context.Context, _ string) (any, bool, error) {
		// Artifact (clip/dvr/vod) first.
		if resp, aerr := b.commodore.ResolveArtifactPlaybackID(ctx, contentID); aerr == nil && resp.GetFound() && resp.GetTenantId() != "" {
			return beaconAttribution{
				tenantID:        resp.GetTenantId(),
				streamID:        resp.GetStreamId(),
				artifactHash:    resp.GetArtifactHash(),
				internalName:    resp.GetInternalName(),
				originClusterID: resp.GetOriginClusterId(),
				contentType:     resp.GetContentType(),
			}, true, nil
		}
		// Live stream.
		if resp, serr := b.commodore.ResolvePlaybackID(ctx, contentID); serr == nil && resp.GetTenantId() != "" {
			return beaconAttribution{
				tenantID:        resp.GetTenantId(),
				streamID:        resp.GetStreamId(),
				internalName:    resp.GetInternalName(),
				originClusterID: resp.GetOriginClusterId(),
				contentType:     "live",
			}, true, nil
		}
		return beaconAttribution{}, false, nil
	})
	if err != nil || !found {
		return beaconAttribution{}, false
	}
	attr, ok := val.(beaconAttribution)
	return attr, ok
}

// clusterClaims verifies a resolve-time telemetry token and returns its claims
// only when the token is valid and its content id matches this beacon. A beacon
// alone cannot prove which endpoint served it, so serving node/cluster are
// trusted only through this signed path.
func (b *BeaconIntake) clusterClaims(contentID, token string) (telemetrytoken.Claims, bool) {
	if len(b.telemetrySecret) == 0 || token == "" {
		return telemetrytoken.Claims{}, false
	}
	claims, err := telemetrytoken.Verify(b.telemetrySecret, token, time.Now())
	if err != nil || claims.ContentID != contentID {
		return telemetrytoken.Claims{}, false
	}
	return claims, true
}

// bindBeaconBody caps the body and decodes JSON into dst, writing 400 on a
// malformed payload (a client bug). Returns true on success.
func bindBeaconBody[T any](c *gin.Context, maxBody int64, dst *T) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid telemetry body"})
		return false
	}
	return true
}

// validContentID trims and bounds a client-supplied content_id.
func validContentID(raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	if id == "" || len(id) > 256 {
		return "", false
	}
	return id, true
}

func setBeaconCORS(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type")
	c.Header("Access-Control-Max-Age", "86400")
}

// redactURL strips the query string and fragment so signed playback tokens
// (?jwt=…) and other credentials never get persisted in analytics. The player
// already redacts client-side; this is the server-side guarantee for any client.
func redactURL(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// newBeaconEventID mints the canonical UUIDv7 dedup key for a beacon. UUIDv7 is
// time-ordered, which keeps ClickHouse/Kafka dedup locality good; v4 is an
// acceptable fallback if v7 generation fails.
func newBeaconEventID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}
