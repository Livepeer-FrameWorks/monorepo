package handlers

import (
	"net/http"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/gin-gonic/gin"
)

// playbackTelemetryMaxBody caps the boot beacon body. Boot traces are small;
// anything larger is a misbehaving or malicious client.
const playbackTelemetryMaxBody = 16 * 1024

// PlaybackTelemetryHandler ingests browser-originated player boot traces.
//
// The browser is untrusted: it sends content_id, ephemeral trace_id/session_id,
// and an optional signed telemetry token. The shared beaconIntake derives
// tenant_id/stream_id/artifact_hash server-side from Commodore, rate-limits per
// IP, and verifies the token; this handler mints the canonical event_id and
// forwards a PlaybackBootTrace to Decklog. Any ownership ids in the body are
// ignored.
//
// Serving node_id/serving_cluster_id are trusted only from a valid telemetry
// token whose content id matches the beacon (a beacon alone cannot prove which
// endpoint served it); without one they stay empty with cluster_attributed=false,
// which excludes the row from cluster-ops aggregates. origin_cluster_id is
// authoritative from Commodore and is always stamped.
type PlaybackTelemetryHandler struct {
	intake  *BeaconIntake
	decklog triggerSink
}

// NewPlaybackTelemetryHandler builds the boot beacon handler on a shared
// BeaconIntake so boot and session beacons resolve attribution through one cache.
func NewPlaybackTelemetryHandler(intake *BeaconIntake, decklogClient triggerSink) *PlaybackTelemetryHandler {
	return &PlaybackTelemetryHandler{
		intake:  intake,
		decklog: decklogClient,
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

// Handle is the POST /playback/telemetry/boot entrypoint.
func (h *PlaybackTelemetryHandler) Handle(c *gin.Context) {
	setBeaconCORS(c)

	if h.intake.rateLimited(c, "bootbeacon:") {
		return
	}

	var body playbackBootBody
	if !bindBeaconBody(c, playbackTelemetryMaxBody, &body) {
		return
	}

	contentID, ok := validContentID(body.ContentID)
	if !ok {
		c.Status(http.StatusNoContent)
		return
	}

	attr, ok := h.intake.resolveAttribution(c.Request.Context(), contentID)
	if !ok || attr.tenantID == "" {
		// Unresolvable playback id — drop quietly.
		c.Status(http.StatusNoContent)
		return
	}

	trigger := h.buildTrigger(contentID, &body, attr)
	if err := h.decklog.SendTriggerContext(c.Request.Context(), trigger); err != nil {
		h.intake.logger.WithError(err).Warn("playback boot telemetry: Decklog send failed")
		// Still 204 — the client neither retries nor learns the backend state.
	}
	c.Status(http.StatusNoContent)
}

// HandleOptions answers CORS preflight for the public beacon.
func (h *PlaybackTelemetryHandler) HandleOptions(c *gin.Context) {
	setBeaconCORS(c)
	c.Status(http.StatusNoContent)
}

func (h *PlaybackTelemetryHandler) buildTrigger(contentID string, body *playbackBootBody, attr beaconAttribution) *ipcpb.MistTrigger {
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
	if claims, ok := h.intake.clusterClaims(contentID, body.TelemetryToken); ok {
		boot.NodeId = claims.NodeID
		boot.ServingClusterId = claims.ServingClusterID
		if claims.OriginClusterID != "" {
			boot.OriginClusterId = claims.OriginClusterID
		}
		boot.ClusterAttributed = true
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
