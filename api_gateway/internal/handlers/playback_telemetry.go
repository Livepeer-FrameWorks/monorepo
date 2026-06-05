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

// playbackTelemetryMaxBody caps the beacon body. Boot traces are small; anything
// larger is a misbehaving or malicious client.
const playbackTelemetryMaxBody = 16 * 1024

// Per-IP rate limit for the public, unauthenticated beacon. Telemetry is lossy
// by design — over-limit requests are dropped (204), never queued.
const (
	playbackTelemetryRateLimit = 120 // requests/min per IP
	playbackTelemetryBurst     = 60
)

// rateLimiter is the minimal slice of middleware.RateLimiter this handler needs,
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

// triggerSink is the slice of the Decklog client used to forward the trace.
type triggerSink interface {
	SendTriggerContext(ctx context.Context, trigger *ipcpb.MistTrigger) error
}

// PlaybackTelemetryHandler ingests browser-originated player boot traces.
//
// The browser is untrusted: it sends content_id, ephemeral trace_id/session_id,
// and an optional signed telemetry token. This handler derives
// tenant_id/stream_id/artifact_hash server-side from Commodore, mints the
// canonical event_id, rate-limits per IP, and forwards a PlaybackBootTrace to
// Decklog. Any ownership ids in the body are ignored.
//
// Serving node_id/serving_cluster_id are trusted only from a valid telemetry
// token whose content id matches the beacon (a beacon alone cannot prove which
// endpoint served it); without one they stay empty with cluster_attributed=false,
// which excludes the row from cluster-ops aggregates. origin_cluster_id is
// authoritative from Commodore and is always stamped.
type PlaybackTelemetryHandler struct {
	commodore       playbackContentResolver
	decklog         triggerSink
	limiter         rateLimiter
	attrCache       *cache.Cache
	telemetrySecret []byte
	logger          logging.Logger
}

func NewPlaybackTelemetryHandler(
	commodoreClient playbackContentResolver,
	decklogClient triggerSink,
	limiter rateLimiter,
	telemetrySecret []byte,
	logger logging.Logger,
) *PlaybackTelemetryHandler {
	// content_id -> attribution. Bridge can't key the Commodore client's own
	// per-tenant cache pre-resolution, so a small content-id cache absorbs many
	// viewers of the same stream.
	attrCache := cache.New(cache.Options{
		TTL:         60 * time.Second,
		NegativeTTL: 30 * time.Second,
		MaxEntries:  10000,
	}, cache.MetricsHooks{})
	return &PlaybackTelemetryHandler{
		commodore:       commodoreClient,
		decklog:         decklogClient,
		limiter:         limiter,
		attrCache:       attrCache,
		telemetrySecret: telemetrySecret,
		logger:          logger,
	}
}

type bootResourceBody struct {
	Kind            string  `json:"kind"`
	URL             string  `json:"url"`
	TtfbMs          uint32  `json:"ttfbMs"`
	DurationMs      uint32  `json:"durationMs"`
	TransferSize    uint64  `json:"transferSize"`
	EncodedBodySize uint64  `json:"encodedBodySize"`
	DecodedBodySize uint64  `json:"decodedBodySize"`
	CacheStatus     string  `json:"cacheStatus"`
	AgeSeconds      *uint32 `json:"ageSeconds"`
}

// playbackBootBody mirrors the player's BootTrace shape (camelCase). Ownership
// fields are deliberately absent — attribution is server-derived.
type playbackBootBody struct {
	TraceID        string `json:"traceId"`
	SessionID      string `json:"sessionId"`
	ContentID      string `json:"contentId"`
	ContentType    string `json:"contentType"`
	IsLive         bool   `json:"isLive"`
	Outcome        string `json:"outcome"`
	ErrorCode      string `json:"errorCode"`
	PlayerType     string `json:"playerType"`
	Protocol       string `json:"protocol"`
	PlayerVersion  string `json:"playerVersion"`
	ConnectionType string `json:"connectionType"`
	TotalTtfMs     uint32 `json:"totalTtfMs"`
	Spans          struct {
		GatewayResolveMs uint32 `json:"gatewayResolveMs"`
		MistHydrateMs    uint32 `json:"mistHydrateMs"`
		PlayerSelectMs   uint32 `json:"playerSelectMs"`
		ConnectMs        uint32 `json:"connectMs"`
		PrebufferMs      uint32 `json:"prebufferMs"`
	} `json:"spans"`
	Resources []bootResourceBody `json:"resources"`
	// TelemetryToken is the signed resolve-time token; when valid and its content
	// id matches, Bridge stamps the serving node/cluster and cluster_attributed.
	TelemetryToken string `json:"telemetryToken"`
}

type bootAttribution struct {
	tenantID        string
	streamID        string
	artifactHash    string
	internalName    string
	originClusterID string
	contentType     string
}

// Handle is the POST /playback/telemetry/boot entrypoint.
func (h *PlaybackTelemetryHandler) Handle(c *gin.Context) {
	setBeaconCORS(c)

	clientIP := c.ClientIP()
	if h.limiter != nil {
		if allowed, _, _ := h.limiter.Allow("bootbeacon:"+clientIP, playbackTelemetryRateLimit, playbackTelemetryBurst); !allowed {
			c.Status(http.StatusNoContent)
			return
		}
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, playbackTelemetryMaxBody)
	var body playbackBootBody
	if err := c.ShouldBindJSON(&body); err != nil {
		// Malformed payload is a client bug; signal it but don't leak detail.
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid telemetry body"})
		return
	}

	contentID := strings.TrimSpace(body.ContentID)
	if contentID == "" || len(contentID) > 256 {
		c.Status(http.StatusNoContent)
		return
	}

	attr, ok := h.resolveAttribution(c.Request.Context(), contentID)
	if !ok || attr.tenantID == "" {
		// Unresolvable playback id — drop quietly.
		c.Status(http.StatusNoContent)
		return
	}

	trigger := h.buildTrigger(contentID, &body, attr)
	if err := h.decklog.SendTriggerContext(c.Request.Context(), trigger); err != nil {
		h.logger.WithError(err).Warn("playback boot telemetry: Decklog send failed")
		// Still 204 — the client neither retries nor learns the backend state.
	}
	c.Status(http.StatusNoContent)
}

// HandleOptions answers CORS preflight for the public beacon.
func (h *PlaybackTelemetryHandler) HandleOptions(c *gin.Context) {
	setBeaconCORS(c)
	c.Status(http.StatusNoContent)
}

func setBeaconCORS(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type")
	c.Header("Access-Control-Max-Age", "86400")
}

// resolveAttribution maps a public content_id to its owning tenant/stream/artifact
// via Commodore (artifact first, then live stream), cached by content_id.
func (h *PlaybackTelemetryHandler) resolveAttribution(ctx context.Context, contentID string) (bootAttribution, bool) {
	val, found, err := h.attrCache.Get(ctx, "boot:attr:"+contentID, func(ctx context.Context, _ string) (any, bool, error) {
		// Artifact (clip/dvr/vod) first.
		if resp, aerr := h.commodore.ResolveArtifactPlaybackID(ctx, contentID); aerr == nil && resp.GetFound() && resp.GetTenantId() != "" {
			return bootAttribution{
				tenantID:        resp.GetTenantId(),
				streamID:        resp.GetStreamId(),
				artifactHash:    resp.GetArtifactHash(),
				internalName:    resp.GetInternalName(),
				originClusterID: resp.GetOriginClusterId(),
				contentType:     resp.GetContentType(),
			}, true, nil
		}
		// Live stream.
		if resp, serr := h.commodore.ResolvePlaybackID(ctx, contentID); serr == nil && resp.GetTenantId() != "" {
			return bootAttribution{
				tenantID:        resp.GetTenantId(),
				streamID:        resp.GetStreamId(),
				internalName:    resp.GetInternalName(),
				originClusterID: resp.GetOriginClusterId(),
				contentType:     "live",
			}, true, nil
		}
		return bootAttribution{}, false, nil
	})
	if err != nil || !found {
		return bootAttribution{}, false
	}
	attr, ok := val.(bootAttribution)
	return attr, ok
}

func (h *PlaybackTelemetryHandler) buildTrigger(contentID string, body *playbackBootBody, attr bootAttribution) *ipcpb.MistTrigger {
	contentType := body.ContentType
	if contentType == "" {
		contentType = attr.contentType
	}

	boot := &ipcpb.PlaybackBootTrace{
		ArtifactHash:      attr.artifactHash,
		InternalName:      attr.internalName,
		OriginClusterId:   attr.originClusterID,
		ClusterAttributed: false,

		ContentId:         contentID,
		SessionId:         body.SessionID,
		TraceId:           body.TraceID,
		ClientTimestampMs: time.Now().UnixMilli(),

		TotalTtfMs:       body.TotalTtfMs,
		GatewayResolveMs: body.Spans.GatewayResolveMs,
		MistHydrateMs:    body.Spans.MistHydrateMs,
		PlayerSelectMs:   body.Spans.PlayerSelectMs,
		ConnectMs:        body.Spans.ConnectMs,
		PrebufferMs:      body.Spans.PrebufferMs,

		Outcome:        body.Outcome,
		ErrorCode:      body.ErrorCode,
		PlayerType:     body.PlayerType,
		Protocol:       body.Protocol,
		ContentType:    contentType,
		IsLive:         body.IsLive,
		ConnectionType: body.ConnectionType,
		PlayerVersion:  body.PlayerVersion,
	}
	if attr.tenantID != "" {
		boot.TenantId = &attr.tenantID
	}
	if attr.streamID != "" {
		boot.StreamId = &attr.streamID
	}

	// Cluster attribution is trusted ONLY from a valid telemetry token whose
	// content id matches this beacon. Without it, node/cluster stay empty and
	// cluster_attributed=false, excluding the row from cluster-ops aggregates.
	if len(h.telemetrySecret) > 0 && body.TelemetryToken != "" {
		if claims, err := telemetrytoken.Verify(h.telemetrySecret, body.TelemetryToken, time.Now()); err == nil && claims.ContentID == contentID {
			boot.NodeId = claims.NodeID
			boot.ServingClusterId = claims.ServingClusterID
			if claims.OriginClusterID != "" {
				boot.OriginClusterId = claims.OriginClusterID
			}
			boot.ClusterAttributed = true
		}
	}

	for _, r := range body.Resources {
		res := &ipcpb.PlaybackBootResource{
			Kind:            r.Kind,
			Url:             redactURL(r.URL),
			TtfbMs:          r.TtfbMs,
			DurationMs:      r.DurationMs,
			TransferSize:    r.TransferSize,
			EncodedBodySize: r.EncodedBodySize,
			DecodedBodySize: r.DecodedBodySize,
		}
		if r.CacheStatus != "" {
			res.CacheStatus = &r.CacheStatus
		}
		if r.AgeSeconds != nil {
			res.AgeSeconds = r.AgeSeconds
		}
		boot.Resources = append(boot.Resources, res)
	}

	trigger := &ipcpb.MistTrigger{
		TriggerType: "PLAYBACK_BOOT_TRACE",
		Timestamp:   time.Now().Unix(),
		EventId:     newBeaconEventID(), // Bridge mints the canonical dedup key
		TriggerPayload: &ipcpb.MistTrigger_PlaybackBootTrace{
			PlaybackBootTrace: boot,
		},
	}
	if attr.tenantID != "" {
		trigger.TenantId = &attr.tenantID
	}
	if attr.streamID != "" {
		trigger.StreamId = &attr.streamID
	}
	if attr.originClusterID != "" {
		trigger.OriginClusterId = &attr.originClusterID
	}
	return trigger
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
