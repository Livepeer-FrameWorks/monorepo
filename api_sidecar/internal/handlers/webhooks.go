package handlers

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
)

var tenantCache = struct {
	mu   sync.RWMutex
	data map[string]string
}{data: make(map[string]string)}

func getTenantForInternalName(internalName string) string {
	if internalName == "" {
		return ""
	}
	tenantCache.mu.RLock()
	if v, ok := tenantCache.data[internalName]; ok {
		tenantCache.mu.RUnlock()
		return v
	}
	tenantCache.mu.RUnlock()

	// Resolve via Commodore service route with service auth
	url := apiBaseURL + "/resolve-internal-name/" + internalName
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ""
	}
	if serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+serviceToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	tenantID := extractJSONField(bodyBytes, "tenant_id")
	if tenantID != "" {
		tenantCache.mu.Lock()
		tenantCache.data[internalName] = tenantID
		tenantCache.mu.Unlock()
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

	// Forward push end event to API for database updates
	go forwardEventToCommodore("push-end", map[string]interface{}{
		"node_id":       nodeID,
		"push_id":       pushID,
		"stream_name":   streamName,
		"internal_name": internalName,
		"target_uri":    targetURIAfter,
		"status":        pushStatus,
		"log_messages":  logMessages,
		"event_type":    "push_end",
		"timestamp":     time.Now().Unix(),
	})

	// Forward analytics event to Decklog for batched processing
	go ForwardEventToDecklog("push-lifecycle", map[string]interface{}{
		"push_id":       pushID,
		"stream_name":   streamName,
		"internal_name": internalName,
		"target_uri":    targetURIAfter,
		"status":        pushStatus,
		"event_type":    "push_end",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_webhook",
		"node_id":       nodeID,
		"tenant_id":     getTenantForInternalName(internalName),
	})

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

	go ForwardEventToDecklog("push-lifecycle", map[string]interface{}{
		"node_id":       nodeID,
		"stream_name":   streamName,
		"internal_name": internalName,
		"push_target":   pushTarget,
		"event_type":    "push_out_start",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_webhook",
		"tenant_id":     getTenantForInternalName(internalName),
	})

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

	periscopeEventData := map[string]interface{}{
		"node_id":       nodeID,
		"stream_name":   streamName,
		"internal_name": internalName,
		"status":        "live", // Set to live when buffer is available
		"buffer_state":  bufferState,
		"event_type":    "stream-buffer",
		"timestamp":     time.Now().Unix(),
		"tenant_id":     getTenantForInternalName(internalName),
	}
	if streamDetails != "" {
		periscopeEventData["stream_details"] = streamDetails
	}

	// Add parsed health metrics if available
	if healthMetrics != nil {
		for key, value := range healthMetrics {
			periscopeEventData[key] = value
		}
	}

	// Forward buffer event to Decklog for analytics
	go ForwardEventToDecklog("stream-buffer", periscopeEventData)

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

	// Forward stream end event to Commodore for database updates
	go forwardEventToCommodore("stream-status", map[string]interface{}{
		"node_id":       nodeID,
		"internal_name": internalName,
		"status":        "offline",
		"buffer_state":  "EMPTY",
		"event_type":    "stream-end",
		"timestamp":     time.Now().Unix(),
	})

	// Forward stream end event to Decklog for analytics
	streamEndData := map[string]interface{}{
		"stream_name":   streamName,
		"internal_name": internalName,
		"buffer_state":  "EMPTY",
		"event_type":    "stream-end",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_webhook",
		"tenant_id":     getTenantForInternalName(internalName),
	}

	// Include stream metrics if available
	if len(params) >= 7 {
		streamEndData["downloaded_bytes"] = downloadedBytes
		streamEndData["uploaded_bytes"] = uploadedBytes
		streamEndData["total_viewers"] = totalViewers
		streamEndData["total_inputs"] = totalInputs
		streamEndData["total_outputs"] = totalOutputs
		streamEndData["viewer_seconds"] = viewerSeconds
	}

	go ForwardEventToDecklog("stream-end", streamEndData)

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

	// Forward analytics event to Decklog for batched processing
	go ForwardEventToDecklog("user-connection", map[string]interface{}{
		"node_id":         nodeID,
		"stream_name":     streamName,
		"internal_name":   internalName,
		"connection_addr": connectionAddr,
		"connection_id":   connectionID,
		"connector":       connector,
		"request_url":     requestURL,
		"session_id":      sessionID,
		"action":          "connect",
		"event_type":      "user_new",
		"timestamp":       time.Now().Unix(),
		"source":          "mistserver_webhook",
		"tenant_id":       getTenantForInternalName(internalName),
	})

	// Return "true" to accept the session
	c.String(http.StatusOK, "true")
}

// HandleUserEnd handles USER_END webhook
// Payload: stream name, connection address, connection identifier, connector, request url, session identifier,
//
//	down bytes, up bytes, seconds connected, unix time connected, unix time disconnected, tags
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
	if len(params) < 12 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
		}).Error("Invalid USER_END payload")
		c.String(http.StatusOK, "OK")
		return
	}

	streamName := params[0]
	connectionAddr := params[1]
	connectionID, _ := strconv.Atoi(params[2])
	connector := params[3]
	requestURL := params[4]
	sessionID, _ := strconv.Atoi(params[5])
	downBytes, _ := strconv.ParseInt(params[6], 10, 64)
	upBytes, _ := strconv.ParseInt(params[7], 10, 64)
	secondsConnected, _ := strconv.Atoi(params[8])
	timeConnected, _ := strconv.ParseInt(params[9], 10, 64)
	timeDisconnected, _ := strconv.ParseInt(params[10], 10, 64)
	tags := params[11]

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

	// Forward analytics event to Decklog for comprehensive analytics processing
	go ForwardEventToDecklog("user-connection", map[string]interface{}{
		"node_id":           nodeID,
		"stream_name":       streamName,
		"internal_name":     internalName,
		"connection_addr":   connectionAddr,
		"connection_id":     connectionID,
		"connector":         connector,
		"request_url":       requestURL,
		"session_id":        sessionID,
		"down_bytes":        downBytes,
		"up_bytes":          upBytes,
		"seconds_connected": secondsConnected,
		"time_connected":    timeConnected,
		"time_disconnected": timeDisconnected,
		"tags":              tags,
		"action":            "disconnect",
		"event_type":        "user_end",
		"timestamp":         time.Now().Unix(),
		"source":            "mistserver_webhook",
		"tenant_id":         getTenantForInternalName(internalName),
	})

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

	// Forward track list update to Decklog with parsed details
	trackEventData := map[string]interface{}{
		"node_id":       nodeID,
		"stream_name":   streamName,
		"internal_name": internalName,
		"track_list":    trackListJSON,
		"event_type":    "track_list_update",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_webhook",
		"tenant_id":     getTenantForInternalName(internalName),
	}

	// Add parsed quality metrics if available
	if qualityMetrics != nil {
		for key, value := range qualityMetrics {
			trackEventData[key] = value
		}
	}

	go ForwardEventToDecklog("track-list", trackEventData)

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

	// Forward bandwidth threshold event to Decklog for analytics
	go ForwardEventToDecklog("bandwidth-threshold", map[string]interface{}{
		"node_id":               nodeID,
		"stream_name":           streamName,
		"internal_name":         internalName,
		"current_bytes_per_sec": currentBytesPerSecond,
		"threshold_exceeded":    true,
		"event_type":            "bandwidth_threshold",
		"timestamp":             time.Now().Unix(),
		"source":                "mistserver_webhook",
		"tenant_id":             getTenantForInternalName(internalName),
	})

	c.String(http.StatusOK, "OK")
}

// HandleRecordingEnd handles RECORDING_END webhook
// Payload: stream name, push target, connector/filetype, bytes recorded, seconds spent recording,
//
//	unix time recording started, unix time recording stopped, total milliseconds of media data recorded,
//	millisecond timestamp of first media packet, millisecond timestamp of last media packet,
//	machine-readable reason for exit, human-readable reason for exit
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
	if len(params) < 12 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    12,
		}).Error("Invalid RECORDING_END payload")
		c.String(http.StatusOK, "OK")
		return
	}

	streamName := params[0]
	pushTarget := params[1]
	connector := params[2]
	bytesRecorded, _ := strconv.ParseInt(params[3], 10, 64)
	secondsSpent, _ := strconv.Atoi(params[4])
	timeStarted, _ := strconv.ParseInt(params[5], 10, 64)
	timeStopped, _ := strconv.ParseInt(params[6], 10, 64)
	totalMilliseconds, _ := strconv.ParseInt(params[7], 10, 64)
	firstPacketTime, _ := strconv.ParseInt(params[8], 10, 64)
	lastPacketTime, _ := strconv.ParseInt(params[9], 10, 64)
	machineReason := params[10]
	humanReason := params[11]

	logger.WithFields(logging.Fields{
		"stream_name":    streamName,
		"push_target":    pushTarget,
		"seconds_spent":  secondsSpent,
		"bytes_recorded": bytesRecorded,
		"human_reason":   humanReason,
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
		"node_id":            nodeID,
		"internal_name":      internalName,
		"is_recording":       false,
		"push_target":        pushTarget,
		"connector":          connector,
		"bytes_recorded":     bytesRecorded,
		"seconds_recording":  secondsSpent,
		"time_started":       timeStarted,
		"time_stopped":       timeStopped,
		"total_milliseconds": totalMilliseconds,
		"first_packet_time":  firstPacketTime,
		"last_packet_time":   lastPacketTime,
		"machine_reason":     machineReason,
		"human_reason":       humanReason,
		"event_type":         "recording_end",
		"timestamp":          time.Now().Unix(),
	})

	// Forward recording end event to Decklog for analytics
	go ForwardEventToDecklog("recording-lifecycle", map[string]interface{}{
		"stream_name":       streamName,
		"internal_name":     internalName,
		"push_target":       pushTarget,
		"bytes_recorded":    bytesRecorded,
		"seconds_recording": secondsSpent,
		"event_type":        "recording_end",
		"timestamp":         time.Now().Unix(),
		"source":            "mistserver_webhook",
		"tenant_id":         getTenantForInternalName(internalName),
	})

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
