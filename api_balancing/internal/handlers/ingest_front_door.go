package handlers

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/triggers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"
)

// HandleIngestFrontDoor resolves the best ingest node for a stream key.
//
// GET returns up to maxIngestEndpoints ranked candidates, with every protocol
// URL, for encoders and scripts that cannot follow a redirect. POST 307s to the
// chosen node's WHIP URL:
// RFC 9725 has WHIP clients follow redirects on the initial POST, and 307
// preserves the method and the SDP offer body.
//
// Admission is resolved through ResolveStreamContext, never ValidateStreamKey.
// The latter claims the 30-second ingest lease whenever a cluster resolves, so
// using it here would let a mere resolution — or an abandoned WHIP attempt —
// take placement for a stream that never starts. PUSH_REWRITE remains the
// enforcement gate and the only push-ingest caller that claims through
// ValidateStreamKey.
func HandleIngestFrontDoor(c *gin.Context) {
	start := time.Now()
	c.Request = c.Request.WithContext(control.MediaRequestContext(c.Request.Context(), "ingest_http"))

	// Set before any return this handler makes: every response is reached via a
	// credential-bearing URL, including the 404/402/403/429/503 paths. CORS
	// middleware applies the same policy to preflights that never reach here.
	c.Header("Cache-Control", "no-store")

	streamKey := strings.TrimSpace(strings.TrimPrefix(c.Param("streamKey"), "/"))
	if streamKey == "" {
		respondPlaybackError(c, http.StatusBadRequest, "MISSING_STREAM_KEY", "Stream key is required", nil)
		return
	}

	// One notion of client identity per request: the limiter, the geo lookup,
	// and the routing event all use it, so a spoofed header cannot buy a fresh
	// bucket while still steering the response.
	//
	// Published to both the gin and request contexts so the shared access and
	// panic logs name the same caller. Without it they fall back to the peer
	// address, which behind a proxy is the proxy — the request would be limited
	// and routed as the publisher but logged as nginx.
	clientIP := trustedClientIP(c)
	if clientIP != "" {
		c.Set(string(ctxkeys.KeyClientIP), clientIP)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkeys.KeyClientIP, clientIP))
	}
	if !enforceIngestRateLimit(c, clientIP) {
		return
	}

	var localContext *commodorepb.ResolveStreamContextResponse
	localHandled := false
	if triggerProcessor != nil {
		var localErr error
		localContext, localHandled, localErr = triggerProcessor.ResolveLocalIngestContext(c.Request.Context(), streamKey)
		if localErr != nil {
			if triggers.IsLocalAuthorityDenied(localErr) || triggers.IsLocalAuthorityExpired(localErr) {
				respondPlaybackError(c, http.StatusServiceUnavailable, "VALIDATION_UNAVAILABLE", "Stream validation is unavailable", nil)
				return
			}
			logger.WithError(localErr).Warn("Local ingest projection unavailable; using connected validation")
			localContext = nil
			localHandled = false
		}
		if localHandled && localContext != nil && !localContext.GetAdmitted() {
			denial := control.EvaluateIngestAdmission(localContext)
			emitIngestRoutingEventFn(&RoutingEvent{
				Status:         "failed",
				Details:        denial.Code,
				InternalName:   localContext.GetInternalName(),
				StreamID:       localContext.GetStreamId(),
				StreamTenantID: localContext.GetTenantId(),
				ClientIP:       clientIP,
				LatencyMs:      float32(time.Since(start).Milliseconds()),
			})
			respondPlaybackError(c, denial.HTTPStatus, denial.Code, denial.Message, nil)
			return
		}
	}

	// No cluster is declared. Everywhere else Foghorn passes its own CLUSTER_ID
	// it is reporting where a stream landed, on the cluster it runs; here it is
	// asking where a publisher may go, and one process serves many virtual
	// media clusters. Commodore answers with the tenant's authorized, healthy
	// cluster_peers envelope, which selection then ranks nodes across.
	var streamCtx *commodorepb.ResolveStreamContextResponse
	var err error
	if commodoreClient != nil {
		streamCtx, err = commodoreClient.ResolveStreamContextByStreamKey(c.Request.Context(), streamKey, "")
	}
	if err != nil || streamCtx == nil {
		streamCtx = localContext
		// The signed outage owner is a fallback placement boundary, not a
		// steady-state live-claim pin. Only apply it when connected runtime
		// placement is actually unavailable.
		if localHandled && streamCtx != nil && streamCtx.GetActiveIngestClusterId() == "" {
			if owner := strings.TrimSpace(streamCtx.GetOriginClusterId()); owner != "" {
				streamCtx.ActiveIngestClusterId = &owner
			}
		}
	}
	if streamCtx == nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Warn("Ingest front door: stream context resolution failed")
		emitIngestRoutingEventFn(&RoutingEvent{
			Status:    "failed",
			Details:   "stream context resolution failed",
			ClientIP:  clientIP,
			LatencyMs: float32(time.Since(start).Milliseconds()),
		})
		respondPlaybackError(c, http.StatusServiceUnavailable, "VALIDATION_UNAVAILABLE",
			"Stream validation is unavailable", nil)
		return
	}

	if denial := control.EvaluateIngestAdmission(streamCtx); denial != nil {
		emitIngestRoutingEventFn(&RoutingEvent{
			Status:         "failed",
			Details:        denial.Code,
			InternalName:   streamCtx.GetInternalName(),
			StreamID:       streamCtx.GetStreamId(),
			StreamTenantID: streamCtx.GetTenantId(),
			ClientIP:       clientIP,
			LatencyMs:      float32(time.Since(start).Milliseconds()),
		})
		respondPlaybackError(c, denial.HTTPStatus, denial.Code, denial.Message, nil)
		return
	}

	// NaN, not zero: (0,0) is a real place off West Africa, so a failed lookup
	// would geo-score every node against it instead of dropping geo from the
	// ranking. The gRPC resolver does the same, and the two must rank alike.
	lat, lon := math.NaN(), math.NaN()
	if geoipReader != nil {
		if geoData := geoip.LookupCached(c.Request.Context(), geoipReader, geoipCache, clientIP); geoData != nil {
			lat = geoData.Latitude
			lon = geoData.Longitude
		}
	}

	response, err := control.ResolveIngestEndpoints(c.Request.Context(), &control.IngestDependencies{
		LB:     lb,
		GeoLat: lat,
		GeoLon: lon,
	}, streamCtx, streamKey)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error":         err,
			"internal_name": streamCtx.GetInternalName(),
		}).Warn("Ingest front door: no ingest-capable nodes")
		emitIngestRoutingEventFn(&RoutingEvent{
			Status:         "failed",
			Details:        "no ingest-capable nodes",
			InternalName:   streamCtx.GetInternalName(),
			StreamID:       streamCtx.GetStreamId(),
			StreamTenantID: streamCtx.GetTenantId(),
			ClientIP:       clientIP,
			ClientLat:      lat,
			ClientLon:      lon,
			LatencyMs:      float32(time.Since(start).Milliseconds()),
		})
		respondPlaybackError(c, http.StatusServiceUnavailable, "NO_INGEST_NODES",
			"No ingest-capable nodes are available", nil)
		return
	}

	primary := response.GetPrimary()
	event := &RoutingEvent{
		InternalName:    streamCtx.GetInternalName(),
		StreamID:        streamCtx.GetStreamId(),
		StreamTenantID:  streamCtx.GetTenantId(),
		ClientIP:        clientIP,
		ClientLat:       lat,
		ClientLon:       lon,
		SelectedNodeID:  primary.GetNodeId(),
		Score:           uint64(primary.GetLoadScore()),
		LatencyMs:       float32(time.Since(start).Milliseconds()),
		CandidatesCount: int32(1 + len(response.GetFallbacks())),
	}

	if c.Request.Method == http.MethodPost {
		whipURL := primary.GetWhipUrl()
		if whipURL == "" {
			respondPlaybackError(c, http.StatusServiceUnavailable, "NO_INGEST_NODES",
				"No ingest-capable nodes are available", nil)
			return
		}
		event.Status = "redirect"
		// The WHIP URL embeds the stream key. Details is persisted verbatim
		// into periscope.routing_decisions, so record the destination node
		// rather than the credential-bearing URL.
		event.Details = "whip:" + primary.GetNodeId()
		event.SelectedNode = primary.GetBaseUrl()
		emitIngestRoutingEventFn(event)
		c.Redirect(http.StatusTemporaryRedirect, whipURL)
		return
	}

	// Anonymous surface: Commodore's finalizeIngestResponse guards the gRPC
	// path, but it never sees this response, so owner-only fields are cleared
	// here instead.
	control.StripSensitiveIngestMetadata(response)

	body, marshalErr := protojson.Marshal(response)
	if marshalErr != nil {
		logger.WithError(marshalErr).Error("Ingest front door: failed to marshal response")
		respondPlaybackError(c, http.StatusInternalServerError, "INTERNAL_ERROR",
			"Failed to encode ingest endpoints", nil)
		return
	}

	event.Status = "success"
	emitIngestRoutingEventFn(event)
	c.Data(http.StatusOK, "application/json", body)
}

// Indirected so tests can capture the event and assert no credential reaches
// the durable routing-decision record.
var emitIngestRoutingEventFn = emitIngestRoutingEvent

func emitIngestRoutingEvent(e *RoutingEvent) {
	if decklogClient == nil {
		return
	}
	e.EventType = "ingest_resolve"
	e.Source = "http"
	// Bounded queue, not a goroutine per event: the send is a blocking RPC, so
	// an outage would otherwise pile up goroutines behind requests that
	// themselves succeeded. Workers bound each send with a deadline.
	enqueueRoutingEvent(nil, e)
}
