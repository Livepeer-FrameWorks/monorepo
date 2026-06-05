package handlers

import (
	"net/http"
	"strings"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/gin-gonic/gin"
)

// playbackSessionMaxBody caps the session beacon body. Session deltas are small;
// the larger ceiling (vs boot) leaves room for the VOD retention histogram that
// folds into this beacon for VOD content.
const playbackSessionMaxBody = 32 * 1024

// PlaybackSessionHandler ingests browser-originated viewer-experienced QoE deltas.
//
// Unlike the one-shot boot trace, a session emits a sequence of these beacons
// (heartbeats + a final on page hide); every counter is an additive delta and
// earlier heartbeats remain useful when the final beacon is lost. Trust model is
// identical to the boot beacon: the shared BeaconIntake derives attribution
// from Commodore, rate-limits per IP, and verifies the telemetry token; this
// handler mints the canonical event_id and forwards a PlaybackSessionQoe to
// Decklog.
type PlaybackSessionHandler struct {
	intake  *BeaconIntake
	decklog triggerSink
}

// NewPlaybackSessionHandler builds the session beacon handler on a shared
// BeaconIntake (see NewPlaybackTelemetryHandler).
func NewPlaybackSessionHandler(intake *BeaconIntake, decklogClient triggerSink) *PlaybackSessionHandler {
	return &PlaybackSessionHandler{
		intake:  intake,
		decklog: decklogClient,
	}
}

// playbackSessionBody mirrors the player's session QoE beacon (camelCase).
// Ownership fields are deliberately absent — attribution is server-derived.
type playbackSessionBody struct {
	ContentID      string `json:"contentId"`
	SessionID      string `json:"sessionId"`
	ContentType    string `json:"contentType"`
	IsLive         bool   `json:"isLive"`
	PlayerType     string `json:"playerType"`
	Protocol       string `json:"protocol"`
	PlayerVersion  string `json:"playerVersion"`
	ConnectionType string `json:"connectionType"`

	BeaconSeq   uint32 `json:"beaconSeq"`
	IsFinal     bool   `json:"isFinal"`
	FlushReason string `json:"flushReason"`

	// Additive deltas for the window since the previous beacon.
	PlayedMs      uint64 `json:"playedMs"`
	RebufferMs    uint64 `json:"rebufferMs"`
	RebufferCount uint32 `json:"rebufferCount"`
	SeekWaitMs    uint64 `json:"seekWaitMs"`

	FrameStatsSupported bool   `json:"frameStatsSupported"`
	FramesDecoded       uint64 `json:"framesDecoded"`
	FramesDropped       uint64 `json:"framesDropped"`
	FramesCorrupted     uint64 `json:"framesCorrupted"`

	FirstFrame bool   `json:"firstFrame"`
	FatalError bool   `json:"fatalError"`
	ErrorCode  string `json:"errorCode"`

	// Delivery quality (time-weighted bitrate, ABR switches, EBVS, live-edge).
	BitrateBpsSeconds  uint64 `json:"bitrateBpsSeconds"`
	AbrUpswitchCount   uint32 `json:"abrUpswitchCount"`
	AbrDownswitchCount uint32 `json:"abrDownswitchCount"`
	PlayIntent         bool   `json:"playIntent"`
	LiveEdgeLatencyMs  uint32 `json:"liveEdgeLatencyMs"`

	// VOD retention histogram (absent for live). Sparse parallel arrays of
	// per-bucket watched-seconds deltas for this window.
	BucketWidthS            uint32    `json:"bucketWidthS"`
	AssetDurationS          uint32    `json:"assetDurationS"`
	RetentionBuckets        []uint32  `json:"retentionBuckets"`
	RetentionSecondsWatched []float32 `json:"retentionSecondsWatched"`
	MaxBucketReached        uint32    `json:"maxBucketReached"`

	// TelemetryToken is the signed resolve-time token; when valid and its content
	// id matches, Bridge stamps the serving node/cluster and cluster_attributed.
	TelemetryToken string `json:"telemetryToken"`
}

// Handle is the POST /playback/telemetry/session entrypoint.
func (h *PlaybackSessionHandler) Handle(c *gin.Context) {
	setBeaconCORS(c)

	// Generous per-IP backstop before we read a body — bounds a client flooding by
	// rotating session ids, without starving a large NAT.
	if h.intake.rateLimitedKey(c, "sessionqoe-ip:"+c.ClientIP(), beaconSessionIPLimit, beaconSessionIPBurst) {
		return
	}

	var body playbackSessionBody
	if !bindBeaconBody(c, playbackSessionMaxBody, &body) {
		return
	}

	contentID, ok := validContentID(body.ContentID)
	if !ok {
		c.Status(http.StatusNoContent)
		return
	}

	// session_id is the dedupe + retention identity (ReplacingMergeTree key, per-session
	// rollups). An empty/oversized one would collapse unrelated sessions together and
	// bypass the per-session limiter — drop the beacon rather than corrupt the data.
	sessionID := strings.TrimSpace(body.SessionID)
	if sessionID == "" || len(sessionID) > 256 {
		c.Status(http.StatusNoContent)
		return
	}
	body.SessionID = sessionID // store the trimmed id (the dedupe key)

	// Per-(IP, session) budget: each viewer session gets its own allowance so many
	// viewers behind one NAT don't starve each other's heartbeats.
	if h.intake.rateLimitedKey(c, "sessionqoe:"+c.ClientIP()+":"+sessionID, beaconSessionLimit, beaconSessionBurst) {
		return
	}

	attr, ok := h.intake.resolveAttribution(c.Request.Context(), contentID)
	if !ok || attr.tenantID == "" {
		c.Status(http.StatusNoContent)
		return
	}

	trigger := h.buildTrigger(contentID, &body, attr)
	if err := h.decklog.SendTriggerContext(c.Request.Context(), trigger); err != nil {
		h.intake.logger.WithError(err).Warn("playback session telemetry: Decklog send failed")
		// Still 204 — the client neither retries nor learns the backend state.
	}
	c.Status(http.StatusNoContent)
}

// HandleOptions answers CORS preflight for the public beacon.
func (h *PlaybackSessionHandler) HandleOptions(c *gin.Context) {
	setBeaconCORS(c)
	c.Status(http.StatusNoContent)
}

func (h *PlaybackSessionHandler) buildTrigger(contentID string, body *playbackSessionBody, attr beaconAttribution) *ipcpb.MistTrigger {
	contentType := body.ContentType
	if contentType == "" {
		contentType = attr.contentType
	}

	qoe := &ipcpb.PlaybackSessionQoe{
		ArtifactHash:      attr.artifactHash,
		InternalName:      attr.internalName,
		OriginClusterId:   attr.originClusterID,
		ClusterAttributed: false,

		ContentId:         contentID,
		SessionId:         body.SessionID,
		ClientTimestampMs: time.Now().UnixMilli(),

		BeaconSeq:   body.BeaconSeq,
		IsFinal:     body.IsFinal,
		FlushReason: body.FlushReason,

		PlayerType:     body.PlayerType,
		Protocol:       body.Protocol,
		ContentType:    contentType,
		IsLive:         body.IsLive,
		ConnectionType: body.ConnectionType,
		PlayerVersion:  body.PlayerVersion,

		PlayedMs:      body.PlayedMs,
		RebufferMs:    body.RebufferMs,
		RebufferCount: body.RebufferCount,
		SeekWaitMs:    body.SeekWaitMs,

		FrameStatsSupported: body.FrameStatsSupported,
		FramesDecoded:       body.FramesDecoded,
		FramesDropped:       body.FramesDropped,
		FramesCorrupted:     body.FramesCorrupted,

		FirstFrame: body.FirstFrame,
		FatalError: body.FatalError,
		ErrorCode:  body.ErrorCode,

		BitrateBpsSeconds:  body.BitrateBpsSeconds,
		AbrUpswitchCount:   body.AbrUpswitchCount,
		AbrDownswitchCount: body.AbrDownswitchCount,
		PlayIntent:         body.PlayIntent,
		LiveEdgeLatencyMs:  body.LiveEdgeLatencyMs,
		// VOD timeline geometry + session reach — carried whenever the client knows
		// the timeline (bucket_width_s > 0), independent of watched buckets. A positive
		// bucket_width_s is the read layer's "real VOD reach sample" presence gate, so a
		// seek-only session still contributes reach + geometry.
		BucketWidthS:     body.BucketWidthS,
		AssetDurationS:   body.AssetDurationS,
		MaxBucketReached: body.MaxBucketReached,
	}

	// VOD retention histogram: parallel arrays must match length or the mapping is
	// ambiguous — drop a malformed histogram rather than mis-attribute buckets.
	if len(body.RetentionBuckets) > 0 && len(body.RetentionBuckets) == len(body.RetentionSecondsWatched) {
		qoe.RetentionBuckets = body.RetentionBuckets
		qoe.RetentionSecondsWatched = body.RetentionSecondsWatched
	}
	if attr.tenantID != "" {
		qoe.TenantId = &attr.tenantID
	}
	if attr.streamID != "" {
		qoe.StreamId = &attr.streamID
	}

	// Cluster attribution is trusted ONLY from a valid telemetry token whose
	// content id matches this beacon (same rule as the boot beacon).
	if claims, ok := h.intake.clusterClaims(contentID, body.TelemetryToken); ok {
		qoe.NodeId = claims.NodeID
		qoe.ServingClusterId = claims.ServingClusterID
		if claims.OriginClusterID != "" {
			qoe.OriginClusterId = claims.OriginClusterID
		}
		qoe.ClusterAttributed = true
	}

	trigger := &ipcpb.MistTrigger{
		TriggerType: "PLAYBACK_SESSION_QOE",
		Timestamp:   time.Now().Unix(),
		EventId:     newBeaconEventID(), // Bridge mints the canonical (Kafka-replay) dedup key
		TriggerPayload: &ipcpb.MistTrigger_PlaybackSessionQoe{
			PlaybackSessionQoe: qoe,
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
