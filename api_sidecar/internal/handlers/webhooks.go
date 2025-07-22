package handlers

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
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
	url := apiBaseURL + "/api/v1/resolve-internal-name/" + internalName
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
func HandlePushEnd(c middleware.Context) {
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
func HandlePushOutStart(c middleware.Context) {
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
func HandleStreamBuffer(c middleware.Context) {
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
	if len(params) >= 3 && bufferState != "EMPTY" {
		streamDetails = params[2] // JSON object with stream details
		logger.WithFields(logging.Fields{
			"stream_name":    streamName,
			"buffer_state":   bufferState,
			"stream_details": streamDetails,
		}).Debug("STREAM_BUFFER stream details JSON")
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

	// Forward buffer event to Decklog for analytics
	go ForwardEventToDecklog("stream-buffer", periscopeEventData)

	c.String(http.StatusOK, "OK")
}

// HandleStreamEnd handles STREAM_END webhook
// Payload: stream name
func HandleStreamEnd(c middleware.Context) {
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

	logger.WithFields(logging.Fields{
		"stream_name": streamName,
	}).Info("Stream ended")

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
	go ForwardEventToDecklog("stream-end", map[string]interface{}{
		"stream_name":   streamName,
		"internal_name": internalName,
		"buffer_state":  "EMPTY",
		"event_type":    "stream-end",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_webhook",
		"tenant_id":     getTenantForInternalName(internalName),
	})

	c.String(http.StatusOK, "OK")
}

// HandleUserNew handles USER_NEW webhook
// Payload: stream name, connection address, connection identifier, connector, request url, session identifier
// Response: "true" to accept session, "false" to deny
func HandleUserNew(c middleware.Context) {
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
func HandleUserEnd(c middleware.Context) {
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
func HandleLiveTrackList(c middleware.Context) {
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
	trackList := params[1] // JSON track list

	logger.WithFields(logging.Fields{
		"stream_name": streamName,
	}).Info("Track list updated for stream")

	logger.WithFields(logging.Fields{
		"stream_name": streamName,
		"track_list":  trackList,
	}).Debug("LIVE_TRACK_LIST JSON data")

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

	// Forward track list update to Decklog
	go ForwardEventToDecklog("track-list", map[string]interface{}{
		"node_id":       nodeID,
		"stream_name":   streamName,
		"internal_name": internalName,
		"track_list":    trackList,
		"event_type":    "track_list_update",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_webhook",
		"tenant_id":     getTenantForInternalName(internalName),
	})

	c.String(http.StatusOK, "OK")
}

// HandleLiveBandwidth handles LIVE_BANDWIDTH webhook
// Payload: stream name, bandwidth data (JSON)
func HandleLiveBandwidth(c middleware.Context) {
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

	c.String(http.StatusOK, "OK")
}

// HandleRecordingEnd handles RECORDING_END webhook
// Payload: stream name, push target, connector/filetype, bytes recorded, seconds spent recording,
//
//	unix time recording started, unix time recording stopped, total milliseconds of media data recorded,
//	millisecond timestamp of first media packet, millisecond timestamp of last media packet,
//	machine-readable reason for exit, human-readable reason for exit
func HandleRecordingEnd(c middleware.Context) {
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
