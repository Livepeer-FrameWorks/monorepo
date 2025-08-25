package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/api/periscope"
	"frameworks/pkg/logging"
	"frameworks/pkg/validation"

	"frameworks/api_sidecar/internal/control"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var tenantCache = struct {
	mu   sync.RWMutex
	data map[string]string
}{data: make(map[string]string)}

// isClipHash checks if a string looks like a clip hash (32-character hex string)
func isClipHash(s string) bool {
	return len(s) == 32 && isHexString(s)
}

// isHexString checks if a string contains only hexadecimal characters
func isHexString(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func getTenantForInternalName(internalName string) string {
	if internalName == "" {
		logger.WithField("internal_name", internalName).Debug("Empty internal name provided to getTenantForInternalName")
		return ""
	}

	// Check cache first
	tenantCache.mu.RLock()
	if v, ok := tenantCache.data[internalName]; ok {
		tenantCache.mu.RUnlock()
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"tenant_id":     v,
		}).Debug("Found tenant ID in cache")
		return v
	}
	tenantCache.mu.RUnlock()

	// Check if this is a VOD clip hash - if so, use Foghorn for resolution
	if isClipHash(internalName) {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
		}).Debug("Detected clip hash, resolving via Foghorn")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resolution, err := control.ResolveClipHash(ctx, internalName)
		if err != nil {
			logger.WithFields(logging.Fields{
				"clip_hash": internalName,
				"error":     err,
			}).Error("Failed to resolve clip hash via Foghorn")
			return ""
		}

		if resolution == nil {
			logger.WithFields(logging.Fields{
				"clip_hash": internalName,
			}).Warn("Clip not found in Foghorn")
			return ""
		}

		// Cache the result
		tenantCache.mu.Lock()
		tenantCache.data[internalName] = resolution.TenantId
		tenantCache.mu.Unlock()

		logger.WithFields(logging.Fields{
			"clip_hash":   internalName,
			"tenant_id":   resolution.TenantId,
			"stream_name": resolution.StreamName,
		}).Info("Successfully resolved clip hash via Foghorn")

		return resolution.TenantId
	}

	// For regular streams, resolve via Commodore service route with service auth
	url := apiBaseURL + "/resolve-internal-name/" + internalName
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"url":           url,
			"error":         err,
		}).Error("Failed to create request for tenant resolution")
		return ""
	}
	if serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+serviceToken)
	}

	logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"url":           url,
		"has_token":     serviceToken != "",
	}).Debug("Resolving tenant ID via Commodore")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"url":           url,
			"error":         err,
		}).Error("Failed to call Commodore for tenant resolution")
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"url":           url,
			"status_code":   resp.StatusCode,
		}).Error("Non-200 response from Commodore for tenant resolution")
		return ""
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"error":         err,
		}).Error("Failed to read response body from Commodore")
		return ""
	}

	logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"response_body": string(bodyBytes),
	}).Debug("Received response from Commodore")

	tenantID := extractJSONField(bodyBytes, "tenant_id")
	if tenantID != "" {
		tenantCache.mu.Lock()
		tenantCache.data[internalName] = tenantID
		tenantCache.mu.Unlock()
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"tenant_id":     tenantID,
		}).Info("Successfully resolved and cached tenant ID")
	} else {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"response_body": string(bodyBytes),
		}).Warn("No tenant_id found in Commodore response")
	}
	return tenantID
}

func extractJSONField(b []byte, key string) string {
	s := string(b)
	needle := "\"" + key + "\":"
	idx := strings.Index(s, needle)
	if idx == -1 {
		return ""
	}
	// naive parse for \"value\"
	sub := s[idx+len(needle):]
	q1 := strings.Index(sub, "\"")
	if q1 == -1 {
		return ""
	}
	sub = sub[q1+1:]
	q2 := strings.Index(sub, "\"")
	if q2 == -1 {
		return ""
	}
	return sub[:q2]
}

// HandlePushEnd handles PUSH_END webhook
// Payload: push ID, stream name, target URI (before/after), last 10 log messages, push status
func HandlePushEnd(c *gin.Context) {
	// Track infrastructure event
	if metrics != nil {
		metrics.InfrastructureEvents.WithLabelValues("push_end").Inc()
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read PUSH_END body")
		c.String(http.StatusOK, "OK")
		return
	}

	// Parse parameters - expecting 6 parameters
	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 6 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    6,
		}).Error("Invalid PUSH_END payload")
		c.String(http.StatusOK, "OK")
		return
	}

	pushID := params[0]
	streamName := params[1]
	targetURIBefore := params[2]
	targetURIAfter := params[3]
	logMessages := params[4] // JSON array string
	pushStatus := params[5]  // JSON object string

	logger.WithFields(logging.Fields{
		"push_id":           pushID,
		"stream_name":       streamName,
		"target_uri_before": targetURIBefore,
		"target_uri_after":  targetURIAfter,
		"status":            pushStatus,
	}).Info("Push ended")

	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		internalName = streamName[plusIndex+1:]
	} else {
		internalName = streamName
	}

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Note: Removed push-end Commodore call - no handler exists in Commodore

	// Create typed PushLifecyclePayload
	pushPayload := &validation.PushLifecyclePayload{
		StreamName:      streamName,
		InternalName:    internalName,
		NodeID:          nodeID,
		TenantID:        getTenantForInternalName(internalName),
		PushID:          pushID,
		PushTarget:      targetURIAfter,
		TargetURIBefore: targetURIBefore,
		TargetURIAfter:  targetURIAfter,
		Status:          pushStatus,
		LogMessages:     logMessages,
		Action:          "end",
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:       uuid.New().String(),
		EventType:     validation.EventPushLifecycle,
		Timestamp:     time.Now().UTC(),
		Source:        "mistserver_webhook",
		InternalName:  &internalName,
		SchemaVersion: "2.0",
		PushLifecycle: pushPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	c.String(http.StatusOK, "OK")
}

// HandlePushOutStart handles PUSH_OUT_START webhook
// Payload: stream name, push target
// Response: non-empty response sets push target, empty response aborts push
func HandlePushOutStart(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read PUSH_OUT_START body")
		c.String(http.StatusOK, "") // Empty response aborts push
		return
	}

	// Parse parameters - expecting 2 parameters
	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 2 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    2,
		}).Error("Invalid PUSH_OUT_START payload")
		c.String(http.StatusOK, "") // Empty response aborts push
		return
	}

	streamName := params[0]
	pushTarget := params[1]

	logger.WithFields(logging.Fields{
		"stream_name": streamName,
		"push_target": pushTarget,
	}).Info("Push out start requested")

	// Extract internal name from stream if it's a wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		internalName = streamName[plusIndex+1:]
	} else {
		internalName = streamName
	}

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Forward push out start event to Commodore and analytics
	go forwardEventToCommodore("push-status", map[string]interface{}{
		"node_id":       nodeID,
		"stream_name":   streamName,
		"internal_name": internalName,
		"push_target":   pushTarget,
		"event_type":    "push_out_start",
		"timestamp":     time.Now().Unix(),
	})

	// Create typed PushLifecyclePayload
	pushPayload := &validation.PushLifecyclePayload{
		StreamName:   streamName,
		InternalName: internalName,
		NodeID:       nodeID,
		TenantID:     getTenantForInternalName(internalName),
		PushTarget:   pushTarget,
		Action:       "start",
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:       uuid.New().String(),
		EventType:     validation.EventPushLifecycle,
		Timestamp:     time.Now().UTC(),
		Source:        "mistserver_webhook",
		InternalName:  &internalName,
		SchemaVersion: "2.0",
		PushLifecycle: pushPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	// Return the original push target to allow the push to proceed
	c.String(http.StatusOK, pushTarget)
}

// HandleStreamBuffer handles STREAM_BUFFER webhook
// Payload: stream name, buffer state (JSON)
func HandleStreamBuffer(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read STREAM_BUFFER body")
		c.String(http.StatusOK, "OK")
		return
	}

	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	logger.WithFields(logging.Fields{
		"param_count": len(params),
	}).Debug("STREAM_BUFFER parsed parameters")

	for i, param := range params {
		logger.WithFields(logging.Fields{
			"param_index": i,
			"param_value": param,
		}).Debug("STREAM_BUFFER parameter")
	}

	if len(params) < 2 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    "at least 2",
		}).Error("Invalid STREAM_BUFFER payload")
		c.String(http.StatusOK, "OK")
		return
	}

	streamName := params[0]
	bufferState := params[1] // FULL, EMPTY, DRY, RECOVER
	var streamDetails string
	var parsedDetails map[string]interface{}
	var healthMetrics map[string]interface{}

	if len(params) >= 3 && bufferState != "EMPTY" {
		streamDetails = params[2] // JSON object with stream details
		logger.WithFields(logging.Fields{
			"stream_name":    streamName,
			"buffer_state":   bufferState,
			"stream_details": streamDetails,
		}).Debug("STREAM_BUFFER stream details JSON")

		// Parse the stream details JSON to extract health metrics
		if err := json.Unmarshal([]byte(streamDetails), &parsedDetails); err != nil {
			logger.WithFields(logging.Fields{
				"error":       err,
				"stream_name": streamName,
				"raw_details": streamDetails,
			}).Error("Failed to parse STREAM_BUFFER JSON details")
		} else {
			healthMetrics = extractStreamHealthMetrics(parsedDetails)
			logger.WithFields(logging.Fields{
				"stream_name":    streamName,
				"health_metrics": healthMetrics,
			}).Debug("Extracted health metrics from STREAM_BUFFER")
		}
	}

	logger.WithFields(logging.Fields{
		"stream_name":  streamName,
		"buffer_state": bufferState,
	}).Info("Stream buffer changed")

	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		// This is a wildcard stream: extract the part after the +
		internalName = streamName[plusIndex+1:]
	} else {
		logger.WithFields(logging.Fields{
			"stream_name": streamName,
		}).Warn("Non-wildcard stream format")
		// For non-wildcard streams, use the stream name as-is
		internalName = streamName
	}

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Immediately update Foghorn based on buffer state
	isHealthy := bufferState == "FULL" || bufferState == "RECOVER"
	details := map[string]interface{}{
		"buffer_state": bufferState,
	}
	if streamDetails != "" {
		details["stream_details"] = streamDetails
	}
	go updateFoghornStreamHealth(streamName, isHealthy, details)

	// Forward buffer state change to API and analytics
	// Also set stream status to live when buffer is available
	eventData := map[string]interface{}{
		"node_id":       nodeID,
		"internal_name": internalName,
		"status":        "live", // Set to live when buffer is available
		"buffer_state":  bufferState,
		"event_type":    "stream-buffer",
		"timestamp":     time.Now().Unix(),
	}
	if streamDetails != "" {
		eventData["stream_details"] = streamDetails
	}

	logger.WithFields(logging.Fields{
		"stream_name":  streamName,
		"buffer_state": bufferState,
	}).Info("Forwarding STREAM_BUFFER to Commodore")
	go forwardEventToCommodore("stream-status", eventData)

	// Create typed StreamLifecyclePayload
	streamPayload := &validation.StreamLifecyclePayload{
		StreamName:   streamName,
		InternalName: internalName,
		NodeID:       nodeID,
		TenantID:     getTenantForInternalName(internalName),
		Status:       "live", // Set to live when buffer is available
		BufferState:  bufferState,
	}

	if streamDetails != "" {
		streamPayload.StreamDetails = streamDetails
	}

	// Add parsed health metrics if available
	if healthMetrics != nil {
		if healthScore, ok := healthMetrics["health_score"].(float64); ok {
			streamPayload.HealthScore = healthScore
		}
		if hasIssues, ok := healthMetrics["has_issues"].(bool); ok {
			streamPayload.HasIssues = hasIssues
		}
		if issuesDesc, ok := healthMetrics["issues_description"].(string); ok {
			streamPayload.IssuesDesc = issuesDesc
		}
		if trackCount, ok := healthMetrics["track_count"].(int); ok {
			streamPayload.TrackCount = trackCount
		}
		if qualityTier, ok := healthMetrics["quality_tier"].(string); ok {
			streamPayload.QualityTier = qualityTier
		}

		// Extract frame timing and quality metrics from first track
		if tracks, ok := healthMetrics["tracks"].([]map[string]interface{}); ok && len(tracks) > 0 {
			primaryTrack := tracks[0] // Use first track as primary

			if codec, ok := primaryTrack["codec"].(string); ok {
				streamPayload.PrimaryCodec = codec
			}
			if bitrate, ok := primaryTrack["bitrate"].(int); ok {
				streamPayload.PrimaryBitrate = bitrate
			}

			var width, height int
			if w, ok := primaryTrack["width"].(int); ok {
				width = w
				streamPayload.PrimaryWidth = width
			}
			if h, ok := primaryTrack["height"].(int); ok {
				height = h
				streamPayload.PrimaryHeight = height
			}

			// Calculate resolution as "WIDTHxHEIGHT" if both width and height exist
			if width > 0 && height > 0 {
				streamPayload.PrimaryResolution = fmt.Sprintf("%dx%d", width, height)
			}

			if fps, ok := primaryTrack["fps"].(float64); ok {
				streamPayload.PrimaryFPS = fps
			}
			if frameJitter, ok := primaryTrack["frame_jitter_ms"].(float64); ok {
				streamPayload.FrameJitterMS = frameJitter
			}
			if keyframeStability, ok := primaryTrack["keyframe_stability_ms"].(float64); ok {
				streamPayload.KeyFrameStabilityMS = keyframeStability
			}
			if frameMSMax, ok := primaryTrack["frame_ms_max"].(float64); ok {
				streamPayload.FrameMSMax = frameMSMax
			}
			if frameMSMin, ok := primaryTrack["frame_ms_min"].(float64); ok {
				streamPayload.FrameMSMin = frameMSMin
			}
			if framesMax, ok := primaryTrack["frames_max"].(int); ok {
				streamPayload.FramesMax = framesMax
			}
			if framesMin, ok := primaryTrack["frames_min"].(int); ok {
				streamPayload.FramesMin = framesMin
			}
			if keyframeMSMax, ok := primaryTrack["keyframe_ms_max"].(float64); ok {
				streamPayload.KeyFrameIntervalMS = keyframeMSMax // Use max as interval
			}
		}
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:         uuid.New().String(),
		EventType:       validation.EventStreamLifecycle,
		Timestamp:       time.Now().UTC(),
		Source:          "mistserver_webhook",
		InternalName:    &internalName,
		SchemaVersion:   "2.0",
		StreamLifecycle: streamPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	c.String(http.StatusOK, "OK")
}

// HandleStreamEnd handles STREAM_END webhook
// Payload: stream name, downloaded bytes, uploaded bytes, total viewers, total inputs, total outputs, viewer seconds
func HandleStreamEnd(c *gin.Context) {
	// Track infrastructure event
	if metrics != nil {
		metrics.InfrastructureEvents.WithLabelValues("stream_end").Inc()
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read STREAM_END body")
		c.String(http.StatusOK, "OK")
		return
	}

	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 1 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    "at least 1",
		}).Error("Invalid STREAM_END payload")
		c.String(http.StatusOK, "OK")
		return
	}

	streamName := params[0]

	// Parse additional STREAM_END metrics if available (7 parameters total)
	var downloadedBytes, uploadedBytes int64
	var totalViewers, totalInputs, totalOutputs int
	var viewerSeconds int64

	if len(params) >= 7 {
		downloadedBytes, _ = strconv.ParseInt(params[1], 10, 64)
		uploadedBytes, _ = strconv.ParseInt(params[2], 10, 64)
		totalViewers, _ = strconv.Atoi(params[3])
		totalInputs, _ = strconv.Atoi(params[4])
		totalOutputs, _ = strconv.Atoi(params[5])
		viewerSeconds, _ = strconv.ParseInt(params[6], 10, 64)

		logger.WithFields(logging.Fields{
			"stream_name":      streamName,
			"downloaded_bytes": downloadedBytes,
			"uploaded_bytes":   uploadedBytes,
			"total_viewers":    totalViewers,
			"viewer_seconds":   viewerSeconds,
		}).Info("Stream ended with metrics")
	} else {
		logger.WithFields(logging.Fields{
			"stream_name": streamName,
			"param_count": len(params),
		}).Warn("STREAM_END with incomplete metrics - missing billing data")
	}

	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		// This is a wildcard stream: extract the part after the +
		internalName = streamName[plusIndex+1:]
	} else {
		logger.WithFields(logging.Fields{
			"stream_name": streamName,
		}).Warn("Non-wildcard stream format")
		// For non-wildcard streams, use the stream name as-is
		internalName = streamName
	}

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Immediately update Foghorn that stream is not healthy
	go updateFoghornStreamHealth(streamName, false, map[string]interface{}{
		"buffer_state": "EMPTY",
		"status":       "offline",
	})

	// Notify Foghorn to stop any active DVR recording for this stream
	go notifyDVRStreamEnd(internalName, nodeID)

	// Forward stream end event to Commodore for database updates
	go forwardEventToCommodore("stream-status", map[string]interface{}{
		"node_id":       nodeID,
		"internal_name": internalName,
		"status":        "offline",
		"buffer_state":  "EMPTY",
		"event_type":    "stream-end",
		"timestamp":     time.Now().Unix(),
	})

	// Create typed StreamLifecyclePayload
	streamPayload := &validation.StreamLifecyclePayload{
		StreamName:   streamName,
		InternalName: internalName,
		NodeID:       nodeID,
		TenantID:     getTenantForInternalName(internalName),
		Status:       "offline",
		BufferState:  "EMPTY",
	}

	// Include stream metrics if available
	if len(params) >= 7 {
		streamPayload.DownloadedBytes = downloadedBytes
		streamPayload.UploadedBytes = uploadedBytes
		streamPayload.TotalViewers = totalViewers
		streamPayload.TotalInputs = totalInputs
		streamPayload.TotalOutputs = totalOutputs
		streamPayload.ViewerSeconds = viewerSeconds
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:         uuid.New().String(),
		EventType:       validation.EventStreamLifecycle,
		Timestamp:       time.Now().UTC(),
		Source:          "mistserver_webhook",
		InternalName:    &internalName,
		SchemaVersion:   "2.0",
		StreamLifecycle: streamPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	c.String(http.StatusOK, "OK")
}

// HandleUserNew handles USER_NEW webhook
// Payload: stream name, connection address, connection identifier, connector, request url, session identifier
// Response: "true" to accept session, "false" to deny
func HandleUserNew(c *gin.Context) {
	// Track infrastructure event
	if metrics != nil {
		metrics.InfrastructureEvents.WithLabelValues("user_connected").Inc()
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read USER_NEW body")
		c.String(http.StatusOK, "false") // Deny session on error
		return
	}

	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 6 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    6,
		}).Error("Invalid USER_NEW payload")
		c.String(http.StatusOK, "false") // Deny session on error
		return
	}

	streamName := params[0]
	connectionAddr := params[1]
	connectionID := params[2]
	connector := params[3]
	requestURL := params[4]
	sessionID := params[5]

	logger.WithFields(logging.Fields{
		"stream_name":     streamName,
		"connection_addr": connectionAddr,
		"connector":       connector,
		"session_id":      sessionID,
	}).Info("New user connected")

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		internalName = streamName[plusIndex+1:]
	} else {
		internalName = streamName
	}

	// Create typed UserConnectionPayload
	userPayload := &validation.UserConnectionPayload{
		ConnectionEvent: periscope.ConnectionEvent{
			EventID:        uuid.New().String(),
			TenantID:       getTenantForInternalName(internalName),
			InternalName:   internalName,
			SessionID:      sessionID,
			ConnectionAddr: connectionAddr,
			Connector:      connector,
			NodeID:         nodeID,
			EventType:      "connect",
		},
		Action:       "connect",
		ConnectionID: connectionID,
		RequestURL:   requestURL,
	}

	// Enrich with geo data using connection_addr
	if geoipReader != nil && connectionAddr != "" {
		geoData := geoipReader.Lookup(connectionAddr)
		if geoData != nil {
			userPayload.CountryCode = geoData.CountryCode
			userPayload.City = geoData.City
			userPayload.Latitude = geoData.Latitude
			userPayload.Longitude = geoData.Longitude
		}
	}

	// Create typed BaseEvent
	eventID := uuid.New().String()
	baseEvent := &validation.BaseEvent{
		EventID:        eventID,
		EventType:      validation.EventUserConnection,
		Timestamp:      time.Now().UTC(),
		Source:         "mistserver_webhook",
		InternalName:   &internalName,
		SchemaVersion:  "2.0",
		UserConnection: userPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	// Return "true" to accept the session
	c.String(http.StatusOK, "true")
}

// HandleUserEnd handles USER_END webhook
// Payload: session identifier (hexadecimal string), stream name (string), connector (string), connection address (string),
//
//	duration in seconds (integer), uploaded bytes total (integer), downloaded bytes total (integer), tags (string)
func HandleUserEnd(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read USER_END body")
		c.String(http.StatusOK, "OK")
		return
	}

	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 8 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
		}).Error("Invalid USER_END payload")
		c.String(http.StatusOK, "OK")
		return
	}

	// CORRECT MistServer USER_END format:
	sessionIdentifier := params[0]                      // session identifier (hexadecimal string)
	streamName := params[1]                             // stream name (string)
	connector := params[2]                              // connector (string)
	connectionAddr := params[3]                         // connection address (string)
	secondsConnected, _ := strconv.Atoi(params[4])      // duration in seconds (integer)
	upBytes, _ := strconv.ParseInt(params[5], 10, 64)   // uploaded bytes total (integer)
	downBytes, _ := strconv.ParseInt(params[6], 10, 64) // downloaded bytes total (integer)
	tags := ""
	if len(params) > 7 {
		tags = params[7] // tags (string)
	}

	logger.WithFields(logging.Fields{
		"stream_name":       streamName,
		"connection_addr":   connectionAddr,
		"seconds_connected": secondsConnected,
		"down_bytes":        downBytes,
		"up_bytes":          upBytes,
	}).Info("User disconnected")

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		internalName = streamName[plusIndex+1:]
	} else {
		internalName = streamName
	}

	// Create typed UserConnectionPayload
	userPayload := &validation.UserConnectionPayload{
		ConnectionEvent: periscope.ConnectionEvent{
			EventID:        uuid.New().String(),
			TenantID:       getTenantForInternalName(internalName),
			InternalName:   internalName,
			ConnectionAddr: connectionAddr,
			Connector:      connector,
			NodeID:         nodeID,
			EventType:      "disconnect",
		},
		Action:            "disconnect",
		SessionIdentifier: sessionIdentifier,
		SecondsConnected:  secondsConnected,
		UploadedBytes:     upBytes,
		DownloadedBytes:   downBytes,
		Tags:              tags,
	}

	// Enrich with geo data using connection_addr
	if geoipReader != nil && connectionAddr != "" {
		geoData := geoipReader.Lookup(connectionAddr)
		if geoData != nil {
			userPayload.CountryCode = geoData.CountryCode
			userPayload.City = geoData.City
			userPayload.Latitude = geoData.Latitude
			userPayload.Longitude = geoData.Longitude
		}
	}

	// Create typed BaseEvent
	eventID := uuid.New().String()
	baseEvent := &validation.BaseEvent{
		EventID:        eventID,
		EventType:      validation.EventUserConnection,
		Timestamp:      time.Now().UTC(),
		Source:         "mistserver_webhook",
		InternalName:   &internalName,
		SchemaVersion:  "2.0",
		UserConnection: userPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	c.String(http.StatusOK, "OK")
}

// HandleLiveTrackList handles LIVE_TRACK_LIST webhook
// Payload: stream name, track list (JSON)
func HandleLiveTrackList(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read LIVE_TRACK_LIST body")
		c.String(http.StatusOK, "OK")
		return
	}

	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	logger.WithFields(logging.Fields{
		"param_count": len(params),
	}).Debug("LIVE_TRACK_LIST parsed parameters")

	for i, param := range params {
		logger.WithFields(logging.Fields{
			"param_index": i,
			"param_value": param,
		}).Debug("LIVE_TRACK_LIST parameter")
	}

	if len(params) < 2 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    2,
		}).Error("Invalid LIVE_TRACK_LIST payload")
		c.String(http.StatusOK, "OK")
		return
	}

	streamName := params[0]
	trackListJSON := params[1] // JSON track list

	// Parse the track list JSON to extract track details
	// MistServer sends an object where keys are track names, values are track data
	var trackObject map[string]interface{}
	var parsedTracks []map[string]interface{}
	var qualityMetrics map[string]interface{}

	if err := json.Unmarshal([]byte(trackListJSON), &trackObject); err != nil {
		logger.WithFields(logging.Fields{
			"error":       err,
			"stream_name": streamName,
			"raw_json":    trackListJSON,
		}).Error("Failed to parse LIVE_TRACK_LIST JSON")
	} else {
		// Convert object format to slice format
		for trackName, trackData := range trackObject {
			if trackMap, ok := trackData.(map[string]interface{}); ok {
				// Add track name to the track data
				trackMap["track_name"] = trackName
				parsedTracks = append(parsedTracks, trackMap)
			}
		}

		qualityMetrics = extractTrackQualityMetrics(parsedTracks)
		logger.WithFields(logging.Fields{
			"stream_name":     streamName,
			"track_count":     len(parsedTracks),
			"quality_metrics": qualityMetrics,
		}).Info("Track list updated for stream")
	}

	logger.WithFields(logging.Fields{
		"stream_name": streamName,
		"track_count": len(parsedTracks),
	}).Info("Track list updated for stream")

	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		internalName = streamName[plusIndex+1:]
	} else {
		logger.WithFields(logging.Fields{
			"stream_name": streamName,
		}).Warn("Non-wildcard stream format")
		// For non-wildcard streams, use the stream name as-is
		internalName = streamName
	}

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Create typed TrackListPayload
	trackPayload := &validation.TrackListPayload{
		StreamName:    streamName,
		InternalName:  internalName,
		NodeID:        nodeID,
		TenantID:      getTenantForInternalName(internalName),
		TrackListJSON: trackListJSON,
	}

	// Add parsed quality metrics if available
	if qualityMetrics != nil {
		if trackCount, ok := qualityMetrics["total_tracks"].(int); ok {
			trackPayload.TrackCount = trackCount
		}
		if videoTrackCount, ok := qualityMetrics["video_track_count"].(int); ok {
			trackPayload.VideoTrackCount = videoTrackCount
		}
		if audioTrackCount, ok := qualityMetrics["audio_track_count"].(int); ok {
			trackPayload.AudioTrackCount = audioTrackCount
		}
		if qualityTier, ok := qualityMetrics["quality_tier"].(string); ok {
			trackPayload.QualityTier = qualityTier
		}
		if primaryWidth, ok := qualityMetrics["primary_width"].(int); ok {
			trackPayload.PrimaryWidth = primaryWidth
		}
		if primaryHeight, ok := qualityMetrics["primary_height"].(int); ok {
			trackPayload.PrimaryHeight = primaryHeight
		}
		if primaryFPS, ok := qualityMetrics["primary_fps"].(float64); ok {
			trackPayload.PrimaryFPS = primaryFPS
		}
		if primaryVideoBitrate, ok := qualityMetrics["primary_video_bitrate"].(int); ok {
			trackPayload.PrimaryVideoBitrate = primaryVideoBitrate
		}
		if primaryVideoCodec, ok := qualityMetrics["primary_video_codec"].(string); ok {
			trackPayload.PrimaryVideoCodec = primaryVideoCodec
		}
		if primaryAudioBitrate, ok := qualityMetrics["primary_audio_bitrate"].(int); ok {
			trackPayload.PrimaryAudioBitrate = primaryAudioBitrate
		}
		if primaryAudioCodec, ok := qualityMetrics["primary_audio_codec"].(string); ok {
			trackPayload.PrimaryAudioCodec = primaryAudioCodec
		}
		if primaryAudioChannels, ok := qualityMetrics["primary_audio_channels"].(int); ok {
			trackPayload.PrimaryAudioChannels = primaryAudioChannels
		}
		if primaryAudioSampleRate, ok := qualityMetrics["primary_audio_sample_rate"].(int); ok {
			trackPayload.PrimaryAudioSampleRate = primaryAudioSampleRate
		}
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:       uuid.New().String(),
		EventType:     validation.EventTrackList,
		Timestamp:     time.Now().UTC(),
		Source:        "mistserver_webhook",
		InternalName:  &internalName,
		SchemaVersion: "2.0",
		TrackList:     trackPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	c.String(http.StatusOK, "OK")
}

// HandleLiveBandwidth handles LIVE_BANDWIDTH webhook
// Payload: stream name, current bytes per second
func HandleLiveBandwidth(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read LIVE_BANDWIDTH body")
		c.String(http.StatusOK, "OK")
		return
	}

	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 2 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    2,
		}).Error("Invalid LIVE_BANDWIDTH payload")
		c.String(http.StatusOK, "OK")
		return
	}

	streamName := params[0]
	currentBytesPerSecondStr := params[1]
	currentBytesPerSecond, err := strconv.ParseInt(currentBytesPerSecondStr, 10, 64)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error":       err,
			"stream_name": streamName,
			"bandwidth":   currentBytesPerSecondStr,
		}).Error("Failed to parse LIVE_BANDWIDTH bytes per second")
		c.String(http.StatusOK, "OK")
		return
	}

	logger.WithFields(logging.Fields{
		"stream_name":           streamName,
		"current_bytes_per_sec": currentBytesPerSecond,
	}).Info("Bandwidth threshold exceeded")

	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		internalName = streamName[plusIndex+1:]
	} else {
		logger.WithFields(logging.Fields{
			"stream_name": streamName,
		}).Warn("Non-wildcard stream format")
		internalName = streamName
	}

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Create typed BandwidthThresholdPayload
	bandwidthPayload := &validation.BandwidthThresholdPayload{
		StreamName:         streamName,
		InternalName:       internalName,
		NodeID:             nodeID,
		TenantID:           getTenantForInternalName(internalName),
		CurrentBytesPerSec: currentBytesPerSecond,
		ThresholdExceeded:  true,
		// Note: MistServer doesn't send the threshold value in webhook payload
		// ThresholdValue is left as 0 since it's not available from the webhook
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:            uuid.New().String(),
		EventType:          validation.EventBandwidthThreshold,
		Timestamp:          time.Now().UTC(),
		Source:             "mistserver_webhook",
		InternalName:       &internalName,
		SchemaVersion:      "2.0",
		BandwidthThreshold: bandwidthPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	c.String(http.StatusOK, "OK")
}

// HandleRecordingEnd handles RECORDING_END webhook
// CORRECT MistServer format: stream name, path to file, output protocol name, bytes written, seconds writing took, unix start time, unix end time, media duration (ms)
func HandleRecordingEnd(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read RECORDING_END body")
		c.String(http.StatusOK, "OK")
		return
	}

	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 8 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    8,
		}).Error("Invalid RECORDING_END payload")
		c.String(http.StatusOK, "OK")
		return
	}

	// CORRECT MistServer RECORDING_END format:
	streamName := params[0]                                   // stream name
	filePath := params[1]                                     // path to file that just finished writing
	outputProtocol := params[2]                               // output protocol name
	bytesWritten, _ := strconv.ParseInt(params[3], 10, 64)    // number of bytes written to file
	secondsWriting, _ := strconv.Atoi(params[4])              // amount of seconds that writing took
	timeStarted, _ := strconv.ParseInt(params[5], 10, 64)     // time of connection start (unix-time)
	timeEnded, _ := strconv.ParseInt(params[6], 10, 64)       // time of connection end (unix-time)
	mediaDurationMs, _ := strconv.ParseInt(params[7], 10, 64) // duration of stream media data (milliseconds)

	logger.WithFields(logging.Fields{
		"stream_name":       streamName,
		"file_path":         filePath,
		"output_protocol":   outputProtocol,
		"bytes_written":     bytesWritten,
		"seconds_writing":   secondsWriting,
		"media_duration_ms": mediaDurationMs,
	}).Info("Recording ended")

	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		// This is a wildcard stream: extract the part after the +
		internalName = streamName[plusIndex+1:]
	} else {
		// For non-wildcard streams, use the stream name as-is
		internalName = streamName
	}

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Forward recording end event to Commodore for database updates
	go forwardEventToCommodore("recording-status", map[string]interface{}{
		"node_id":           nodeID,
		"internal_name":     internalName,
		"is_recording":      false,
		"file_path":         filePath,
		"output_protocol":   outputProtocol,
		"bytes_written":     bytesWritten,
		"seconds_writing":   secondsWriting,
		"time_started":      timeStarted,
		"time_ended":        timeEnded,
		"media_duration_ms": mediaDurationMs,
		"event_type":        "recording_end",
		"timestamp":         time.Now().Unix(),
	})

	// Create typed RecordingPayload
	recordingPayload := &validation.RecordingPayload{
		StreamName:      streamName,
		InternalName:    internalName,
		NodeID:          nodeID,
		TenantID:        getTenantForInternalName(internalName),
		FilePath:        filePath,
		OutputProtocol:  outputProtocol,
		BytesWritten:    bytesWritten,
		SecondsWriting:  secondsWriting,
		TimeStarted:     timeStarted,
		TimeEnded:       timeEnded,
		MediaDurationMs: mediaDurationMs,
		IsRecording:     false, // Always false for RECORDING_END
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:       uuid.New().String(),
		EventType:     validation.EventRecordingLifecycle,
		Timestamp:     time.Now().UTC(),
		Source:        "mistserver_webhook",
		InternalName:  &internalName,
		SchemaVersion: "2.0",
		Recording:     recordingPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	c.String(http.StatusOK, "OK")
}

// extractStreamHealthMetrics parses MistServer stream details JSON and extracts health metrics
func extractStreamHealthMetrics(details map[string]interface{}) map[string]interface{} {
	metrics := make(map[string]interface{})
	var tracks []map[string]interface{}

	// Extract issues string if present
	if issues, ok := details["issues"].(string); ok {
		metrics["issues_description"] = issues
		metrics["has_issues"] = true
	} else {
		metrics["has_issues"] = false
	}

	// Process each track to extract codec, quality, and jitter metrics
	for trackID, trackData := range details {
		if trackID == "issues" {
			continue // Skip issues field
		}

		if track, ok := trackData.(map[string]interface{}); ok {
			trackInfo := map[string]interface{}{
				"track_id": trackID,
			}

			// Extract basic track info
			if codec, ok := track["codec"].(string); ok {
				trackInfo["codec"] = codec
			}
			if kbits, ok := track["kbits"].(float64); ok {
				trackInfo["bitrate"] = int(kbits)
			}
			if fpks, ok := track["fpks"].(float64); ok {
				trackInfo["fps"] = fpks / 1000.0 // Convert from fpks to fps
			}
			if height, ok := track["height"].(float64); ok {
				trackInfo["height"] = int(height)
			}
			if width, ok := track["width"].(float64); ok {
				trackInfo["width"] = int(width)
			}
			if channels, ok := track["channels"].(float64); ok {
				trackInfo["channels"] = int(channels)
			}
			if rate, ok := track["rate"].(float64); ok {
				trackInfo["sample_rate"] = int(rate)
			}

			// Extract frame stability metrics from keys
			if keys, ok := track["keys"].(map[string]interface{}); ok {
				if frameMax, ok := keys["frame_ms_max"].(float64); ok {
					trackInfo["frame_ms_max"] = frameMax
				}
				if frameMin, ok := keys["frame_ms_min"].(float64); ok {
					trackInfo["frame_ms_min"] = frameMin
				}
				if framesMax, ok := keys["frames_max"].(float64); ok {
					trackInfo["frames_max"] = int(framesMax)
				}
				if framesMin, ok := keys["frames_min"].(float64); ok {
					trackInfo["frames_min"] = int(framesMin)
				}
				if msMax, ok := keys["ms_max"].(float64); ok {
					trackInfo["keyframe_ms_max"] = msMax
				}
				if msMin, ok := keys["ms_min"].(float64); ok {
					trackInfo["keyframe_ms_min"] = msMin
				}

				// Calculate jitter metrics
				if frameMax, okMax := keys["frame_ms_max"].(float64); okMax {
					if frameMin, okMin := keys["frame_ms_min"].(float64); okMin {
						jitter := frameMax - frameMin
						trackInfo["frame_jitter_ms"] = jitter
					}
				}

				if msMax, okMax := keys["ms_max"].(float64); okMax {
					if msMin, okMin := keys["ms_min"].(float64); okMin {
						keyframeStability := msMax - msMin
						trackInfo["keyframe_stability_ms"] = keyframeStability
					}
				}
			}

			tracks = append(tracks, trackInfo)
		}
	}

	metrics["tracks"] = tracks
	metrics["track_count"] = len(tracks)

	// Calculate overall health score
	healthScore := calculateHealthScore(metrics)
	metrics["health_score"] = healthScore

	return metrics
}

// calculateHealthScore computes a health score (0-100) based on stream metrics
func calculateHealthScore(metrics map[string]interface{}) float64 {
	score := 100.0

	// Deduct points for issues
	if hasIssues, ok := metrics["has_issues"].(bool); ok && hasIssues {
		score -= 30.0 // Issues indicate serious problems
	}

	// Deduct points for high jitter
	if tracks, ok := metrics["tracks"].([]map[string]interface{}); ok {
		maxJitter := 0.0
		for _, track := range tracks {
			if jitter, ok := track["frame_jitter_ms"].(float64); ok {
				maxJitter = math.Max(maxJitter, jitter)
			}
		}

		// Deduct up to 40 points for jitter (0ms = 0 points, 100ms+ = 40 points)
		if maxJitter > 0 {
			jitterPenalty := math.Min(40.0, maxJitter*0.4)
			score -= jitterPenalty
		}
	}

	// Ensure score doesn't go below 0
	return math.Max(0.0, score)
}

// extractTrackQualityMetrics parses LIVE_TRACK_LIST JSON and extracts quality metrics
func extractTrackQualityMetrics(tracks []map[string]interface{}) map[string]interface{} {
	metrics := make(map[string]interface{})
	var videoTracks, audioTracks []map[string]interface{}

	// Process each track in the list
	for i, track := range tracks {
		trackInfo := map[string]interface{}{
			"track_index": i,
		}

		// Extract track ID if present
		if trackID, ok := track["trackid"].(float64); ok {
			trackInfo["track_id"] = int(trackID)
		}

		// Extract track type (video/audio)
		trackType := ""
		if typeVal, ok := track["type"].(string); ok {
			trackType = typeVal
			trackInfo["type"] = typeVal
		}

		// Extract codec
		if codec, ok := track["codec"].(string); ok {
			trackInfo["codec"] = codec
		}

		// Extract video-specific fields
		if width, ok := track["width"].(float64); ok {
			trackInfo["width"] = int(width)
		}
		if height, ok := track["height"].(float64); ok {
			trackInfo["height"] = int(height)
		}
		if fpks, ok := track["fpks"].(float64); ok {
			trackInfo["fps"] = fpks / 1000.0 // Convert from fpks to fps
		}

		// Extract audio-specific fields
		if channels, ok := track["channels"].(float64); ok {
			trackInfo["channels"] = int(channels)
		}
		if rate, ok := track["rate"].(float64); ok {
			trackInfo["sample_rate"] = int(rate)
		}

		// Extract bitrate if present
		if bps, ok := track["bps"].(float64); ok {
			trackInfo["bitrate"] = int(bps)
		}

		// Categorize by type
		if trackType == "video" {
			videoTracks = append(videoTracks, trackInfo)
		} else if trackType == "audio" {
			audioTracks = append(audioTracks, trackInfo)
		}
	}

	metrics["video_tracks"] = videoTracks
	metrics["audio_tracks"] = audioTracks
	metrics["total_tracks"] = len(tracks)
	metrics["video_track_count"] = len(videoTracks)
	metrics["audio_track_count"] = len(audioTracks)

	// Extract primary video quality if available
	if len(videoTracks) > 0 {
		primaryVideo := videoTracks[0]
		if width, ok := primaryVideo["width"].(int); ok {
			metrics["primary_width"] = width
		}
		if height, ok := primaryVideo["height"].(int); ok {
			metrics["primary_height"] = height

			// Calculate quality tier
			if height >= 1080 {
				metrics["quality_tier"] = "1080p+"
			} else if height >= 720 {
				metrics["quality_tier"] = "720p"
			} else if height >= 480 {
				metrics["quality_tier"] = "480p"
			} else {
				metrics["quality_tier"] = "SD"
			}
		}
		if fps, ok := primaryVideo["fps"].(float64); ok {
			metrics["primary_fps"] = fps
		}
		if codec, ok := primaryVideo["codec"].(string); ok {
			metrics["primary_video_codec"] = codec
		}
		if bitrate, ok := primaryVideo["bitrate"].(int); ok {
			metrics["primary_video_bitrate"] = bitrate
		}
	}

	// Extract primary audio info if available
	if len(audioTracks) > 0 {
		primaryAudio := audioTracks[0]
		if channels, ok := primaryAudio["channels"].(int); ok {
			metrics["primary_audio_channels"] = channels
		}
		if sampleRate, ok := primaryAudio["sample_rate"].(int); ok {
			metrics["primary_audio_sample_rate"] = sampleRate
		}
		if codec, ok := primaryAudio["codec"].(string); ok {
			metrics["primary_audio_codec"] = codec
		}
		if bitrate, ok := primaryAudio["bitrate"].(int); ok {
			metrics["primary_audio_bitrate"] = bitrate
		}
	}

	return metrics
}

// notifyDVRStreamEnd notifies Foghorn that a stream has ended and DVR recording should stop
func notifyDVRStreamEnd(internalName, nodeID string) {
	// Create a DVR stop request and send to Foghorn via gRPC
	if err := control.SendDVRStreamEndNotification(internalName, nodeID); err != nil {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"node_id":       nodeID,
			"error":         err,
		}).Error("Failed to send DVR stream end notification via gRPC control channel")
		return
	}

	logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"node_id":       nodeID,
	}).Info("Successfully sent DVR stream end notification via gRPC control channel")
}
