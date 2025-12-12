package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

// HandlerMetrics holds the metrics for handler operations
type HandlerMetrics struct {
	NodeOperations             *prometheus.CounterVec
	InfrastructureEvents       *prometheus.CounterVec
	NodeHealthChecks           *prometheus.CounterVec
	ResourceAllocationDuration *prometheus.HistogramVec
}

var (
	logger   logging.Logger
	metrics  *HandlerMetrics
	nodeName string
)

// Init initializes the handlers with logger, metrics, and node identity.
// nodeID should be passed from the config system to ensure consistency.
func Init(log logging.Logger, m *HandlerMetrics, nodeID string) {
	logger = log
	metrics = m
	nodeName = nodeID

	// Initialize Prometheus monitoring
	InitPrometheusMonitor(logger)

	// Perform initial artifact scan
	if storagePath := os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH"); storagePath != "" {
		totalBytes, artifactCount := scanLocalArtifacts(storagePath)
		logger.WithFields(logging.Fields{
			"storage_path": storagePath,
			"artifacts":    artifactCount,
			"bytes":        totalBytes,
		}).Info("Initial artifact scan completed")
	}

	// Initialize Mist config manager
	config.InitManager(logger)

	// On gRPC seed request, trigger immediate JSON emission (no re-add)
	control.SetOnSeed(func() {
		TriggerImmediatePoll()
	})

	logger.WithField("node_name", nodeName).Info("Handlers initialized")
}

// HealthCheck handles health check requests
func HealthCheck(c *gin.Context) {
	// Track health check
	if metrics != nil {
		metrics.NodeHealthChecks.WithLabelValues("success").Inc()
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "healthy",
		"service": "helmsman",
		"node":    nodeName,
	})
}

// GetPrometheusPassword handles the /koekjes endpoint for Prometheus scraping
func GetPrometheusPassword(c *gin.Context) {
	password := os.Getenv("MIST_PASSWORD")
	if password == "" {
		password = "koekjes"
	}

	c.String(http.StatusOK, password)
}

// getCurrentNodeID gets the current node ID from the prometheus monitor
func getCurrentNodeID() string {
	if prometheusMonitor == nil {
		logger.Warn("PrometheusMonitor is nil in getCurrentNodeID")
		return "unknown"
	}

	// Direct access to nodeID - more reliable than GetNodes()
	prometheusMonitor.mutex.RLock()
	nodeID := prometheusMonitor.nodeID
	prometheusMonitor.mutex.RUnlock()

	if nodeID == "" {
		logger.WithFields(logging.Fields{
			"prometheus_monitor": prometheusMonitor != nil,
		}).Warn("PrometheusMonitor nodeID is empty")
		return "unknown"
	}

	logger.WithFields(logging.Fields{
		"node_id": nodeID,
	}).Debug("Retrieved node ID from PrometheusMonitor")

	return nodeID
}

// HandlePushRewrite handles the PUSH_REWRITE trigger from MistServer
// This is a critical blocking trigger - validates stream keys and routes to wildcard streams
func HandlePushRewrite(c *gin.Context) {
	start := time.Now()

	// Track node operations
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("push_rewrite", "requested").Inc()
	}

	// Read the raw body - MistServer sends parameters as raw text, not JSON
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read PUSH_REWRITE body")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("push_rewrite", "error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response denies the push
		return
	}

	logger.WithFields(logging.Fields{
		"trigger_type": "PUSH_REWRITE",
		"payload_size": len(body),
	}).Debug("Forwarding PUSH_REWRITE trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerPushRewrite, body, control.GetCurrentNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse PUSH_REWRITE trigger")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("push_rewrite", "parse_error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response denies the push
		return
	}

	// Forward trigger to Foghorn via gRPC and get response
	response, shouldAbort, err := control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward PUSH_REWRITE to Foghorn")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("push_rewrite", "forwarding_error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response denies the push
		return
	}

	if shouldAbort {
		logger.WithFields(logging.Fields{
			"response": response,
		}).Info("PUSH_REWRITE aborted by Foghorn")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("push_rewrite", "aborted").Inc()
		}

		c.String(http.StatusOK, "") // Empty response denies the push
		return
	}

	logger.WithFields(logging.Fields{
		"response": response,
	}).Info("PUSH_REWRITE approved by Foghorn")

	// Track successful operation
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("push_rewrite", "success").Inc()
		metrics.ResourceAllocationDuration.WithLabelValues("stream_allocation").Observe(time.Since(start).Seconds())
		metrics.InfrastructureEvents.WithLabelValues("stream_allocated").Inc()
	}

	// Return Foghorn's response to MistServer
	c.String(http.StatusOK, response)
}

// HandlePlayRewrite handles the PLAY_REWRITE trigger from MistServer
// This is a critical blocking trigger - maps playback IDs to internal stream names for viewing (live streams)
// or clip hashes to VOD streams for clip viewing
func HandlePlayRewrite(c *gin.Context) {
	start := time.Now()

	// Track node operations
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("play_rewrite", "requested").Inc()
	}

	// Read the raw body - MistServer sends parameters as raw text, not JSON
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read PLAY_REWRITE body")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("play_rewrite", "error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response uses default behavior
		return
	}

	logger.WithFields(logging.Fields{
		"trigger_type": "PLAY_REWRITE",
		"payload_size": len(body),
	}).Debug("Forwarding PLAY_REWRITE trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerPlayRewrite, body, control.GetCurrentNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse PLAY_REWRITE trigger")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("play_rewrite", "parse_error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response uses default behavior
		return
	}

	// Forward trigger to Foghorn via gRPC and get response
	response, shouldAbort, err := control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward PLAY_REWRITE to Foghorn")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("play_rewrite", "forwarding_error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response uses default behavior
		return
	}

	if shouldAbort {
		logger.WithFields(logging.Fields{
			"response": response,
		}).Info("PLAY_REWRITE aborted by Foghorn")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("play_rewrite", "aborted").Inc()
		}

		c.String(http.StatusOK, "") // Empty response uses default behavior
		return
	}

	logger.WithFields(logging.Fields{
		"response": response,
	}).Info("PLAY_REWRITE resolved by Foghorn")

	// Track successful operation
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("play_rewrite", "success").Inc()
		metrics.ResourceAllocationDuration.WithLabelValues("stream_resolution").Observe(time.Since(start).Seconds())
		metrics.InfrastructureEvents.WithLabelValues("stream_resolved").Inc()
	}

	// Return Foghorn's response to MistServer
	c.String(http.StatusOK, response)
}

// HandleStreamSource handles the STREAM_SOURCE trigger from MistServer
// This is a critical blocking trigger - resolves VOD stream names (vod+{artifact_hash}) to actual file paths for playback
// Supports both clip hashes (mp4 files) and DVR hashes (m3u8 manifests)
func HandleStreamSource(c *gin.Context) {
	start := time.Now()

	// Track node operations
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("stream_source", "requested").Inc()
	}

	// Read the raw body - MistServer sends parameters as raw text, not JSON
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read STREAM_SOURCE body")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("stream_source", "error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response will cause MistServer to use default source
		return
	}

	logger.WithFields(logging.Fields{
		"trigger_type": "STREAM_SOURCE",
		"payload_size": len(body),
	}).Debug("Forwarding STREAM_SOURCE trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerStreamSource, body, control.GetCurrentNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse STREAM_SOURCE trigger")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("stream_source", "parse_error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response will cause MistServer to use default source
		return
	}

	// Forward trigger to Foghorn via gRPC and get response
	response, shouldAbort, err := control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward STREAM_SOURCE to Foghorn")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("stream_source", "forwarding_error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response will cause MistServer to use default source
		return
	}

	if shouldAbort {
		logger.WithFields(logging.Fields{
			"response": response,
		}).Info("STREAM_SOURCE aborted by Foghorn")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("stream_source", "aborted").Inc()
		}

		c.String(http.StatusOK, "") // Empty response will cause MistServer to use default source
		return
	}

	logger.WithFields(logging.Fields{
		"response": response,
	}).Info("STREAM_SOURCE resolved by Foghorn")

	// Track successful operation
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("stream_source", "success").Inc()
		metrics.ResourceAllocationDuration.WithLabelValues("vod_resolution").Observe(time.Since(start).Seconds())
		metrics.InfrastructureEvents.WithLabelValues("source_resolved").Inc()
	}

	// Return Foghorn's response to MistServer
	c.String(http.StatusOK, response)
}

// getNodeID returns the current node ID for building triggers
func getNodeID() string {
	return control.GetCurrentNodeID()
}

// HandlePushEnd handles PUSH_END webhook
// This is a non-blocking trigger that logs push completion status
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

	logger.WithFields(logging.Fields{
		"trigger_type": "PUSH_END",
		"payload_size": len(body),
	}).Debug("Forwarding PUSH_END trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerPushEnd, body, getNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse PUSH_END trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward PUSH_END to Foghorn")
	}

	c.String(http.StatusOK, "OK")
}

// HandlePushOutStart handles PUSH_OUT_START webhook
// This is a blocking trigger - validates and routes outbound pushes
func HandlePushOutStart(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read PUSH_OUT_START body")
		c.String(http.StatusOK, "") // Empty response aborts push
		return
	}

	logger.WithFields(logging.Fields{
		"trigger_type": "PUSH_OUT_START",
		"payload_size": len(body),
	}).Debug("Forwarding PUSH_OUT_START trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerPushOutStart, body, getNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse PUSH_OUT_START trigger")
		c.String(http.StatusOK, "") // Empty response aborts push
		return
	}

	// Forward trigger to Foghorn via gRPC and get response
	response, shouldAbort, err := control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward PUSH_OUT_START to Foghorn")
		c.String(http.StatusOK, "") // Empty response aborts push
		return
	}

	if shouldAbort {
		logger.WithFields(logging.Fields{
			"response": response,
		}).Info("PUSH_OUT_START aborted by Foghorn")
		c.String(http.StatusOK, "") // Empty response aborts push
		return
	}

	logger.WithFields(logging.Fields{
		"response": response,
	}).Info("PUSH_OUT_START approved by Foghorn")

	// Return Foghorn's response to MistServer
	c.String(http.StatusOK, response)
}

// HandleStreamBuffer handles STREAM_BUFFER webhook
// This is a non-blocking trigger that monitors stream buffer state and health
func HandleStreamBuffer(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read STREAM_BUFFER body")
		c.String(http.StatusOK, "OK")
		return
	}

	logger.WithFields(logging.Fields{
		"trigger_type": "STREAM_BUFFER",
		"payload_size": len(body),
	}).Debug("Forwarding STREAM_BUFFER trigger to Foghorn via gRPC")

	// Parse raw webhook data to protobuf
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerStreamBuffer, body, getNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse STREAM_BUFFER trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Enrich with Helmsman-specific metrics
	if sb := mistTrigger.GetStreamBuffer(); sb != nil {
		enrichStreamBufferTrigger(sb)
	}

	// Forward enriched trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward STREAM_BUFFER to Foghorn")
	}

	c.String(http.StatusOK, "OK")
}

// HandleStreamEnd handles STREAM_END webhook
// This is a non-blocking trigger that reports stream end metrics
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

	logger.WithFields(logging.Fields{
		"trigger_type": "STREAM_END",
		"payload_size": len(body),
	}).Debug("Forwarding STREAM_END trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerStreamEnd, body, getNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse STREAM_END trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward STREAM_END to Foghorn")
	}

	c.String(http.StatusOK, "OK")
}

// HandleUserNew handles USER_NEW webhook
// This is a blocking trigger that validates new viewer connections
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

	logger.WithFields(logging.Fields{
		"trigger_type": "USER_NEW",
		"payload_size": len(body),
	}).Debug("Forwarding USER_NEW trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerUserNew, body, getNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse USER_NEW trigger")
		c.String(http.StatusOK, "false") // Deny session on error
		return
	}

	// Forward trigger to Foghorn via gRPC and get response
	response, shouldAbort, err := control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward USER_NEW to Foghorn")
		c.String(http.StatusOK, "false") // Deny session on error
		return
	}

	if shouldAbort {
		logger.WithFields(logging.Fields{
			"response": response,
		}).Info("USER_NEW denied by Foghorn")
		c.String(http.StatusOK, "false") // Deny session
		return
	}

	logger.WithFields(logging.Fields{
		"response": response,
	}).Info("USER_NEW approved by Foghorn")

	// Return Foghorn's response to MistServer
	c.String(http.StatusOK, response)
}

// HandleUserEnd handles USER_END webhook
// This is a non-blocking trigger that reports viewer disconnection metrics
func HandleUserEnd(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read USER_END body")
		c.String(http.StatusOK, "OK")
		return
	}

	logger.WithFields(logging.Fields{
		"trigger_type": "USER_END",
		"payload_size": len(body),
	}).Debug("Forwarding USER_END trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerUserEnd, body, getNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse USER_END trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward USER_END to Foghorn")
	}

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

	logger.WithFields(logging.Fields{
		"trigger_type": "LIVE_TRACK_LIST",
		"payload_size": len(body),
	}).Debug("Forwarding LIVE_TRACK_LIST trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerLiveTrackList, body, getNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse LIVE_TRACK_LIST trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Enrich with track list specific metrics
	if tp := mistTrigger.GetTrackList(); tp != nil {
		enrichLiveTrackListTrigger(tp)
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward LIVE_TRACK_LIST to Foghorn")
	}

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

	logger.WithFields(logging.Fields{
		"trigger_type": "LIVE_BANDWIDTH",
		"payload_size": len(body),
	}).Debug("Forwarding LIVE_BANDWIDTH trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerLiveBandwidth, body, getNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse LIVE_BANDWIDTH trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward LIVE_BANDWIDTH to Foghorn")
	}

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

	logger.WithFields(logging.Fields{
		"trigger_type": "RECORDING_END",
		"payload_size": len(body),
	}).Debug("Forwarding RECORDING_END trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerRecordingEnd, body, getNodeID(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse RECORDING_END trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward RECORDING_END to Foghorn")
	}

	c.String(http.StatusOK, "OK")
}

// enrichStreamBufferTrigger computes Helmsman-specific metrics from parsed tracks
func enrichStreamBufferTrigger(trigger *pb.StreamBufferTrigger) {
	if trigger == nil {
		return
	}

	tracks := trigger.Tracks
	if tracks != nil {
		trackCount := int32(len(tracks))
		trigger.TrackCount = &trackCount
	}

	// Start with MistServer's native issues (primary source of truth)
	// e.g., "HLSnoaudio!", "VeryLowBuffer", etc.
	hasIssues := false
	var issuesDesc []string

	if mistIssues := trigger.GetMistIssues(); mistIssues != "" {
		hasIssues = true
		issuesDesc = append(issuesDesc, mistIssues)
	}

	// Optionally append Helmsman's derived analysis (supplementary diagnostics)
	for _, track := range tracks {
		// Check for high jitter (>100ms is concerning)
		if track.Jitter != nil && *track.Jitter > 100 {
			hasIssues = true
			issuesDesc = append(issuesDesc, "High jitter on track "+track.TrackName)
		}
		// Check for low buffer (<50 is concerning)
		if track.Buffer != nil && *track.Buffer < 50 {
			hasIssues = true
			issuesDesc = append(issuesDesc, "Low buffer on track "+track.TrackName)
		}
	}

	// Set issue indicators
	trigger.HasIssues = &hasIssues
	if len(issuesDesc) > 0 {
		issues := strings.Join(issuesDesc, "; ")
		trigger.IssuesDescription = &issues
	}

	// Determine quality tier from tracks
	if tracks != nil {
		qualityTier := determineQualityTier(tracks)
		if qualityTier != "" {
			trigger.QualityTier = &qualityTier
		}
	}
}

// enrichLiveTrackListTrigger computes quality metrics and primary track info from tracks
func enrichLiveTrackListTrigger(trigger *pb.StreamTrackListTrigger) {
	if trigger == nil || trigger.Tracks == nil {
		return
	}

	tracks := trigger.Tracks
	totalTracks := int32(len(tracks))
	trigger.TotalTracks = &totalTracks

	var videoTracks, audioTracks []*pb.StreamTrack
	for _, track := range tracks {
		if track.TrackType == "video" {
			videoTracks = append(videoTracks, track)
		} else if track.TrackType == "audio" {
			audioTracks = append(audioTracks, track)
		}
	}

	videoTrackCount := int32(len(videoTracks))
	audioTrackCount := int32(len(audioTracks))
	trigger.VideoTrackCount = &videoTrackCount
	trigger.AudioTrackCount = &audioTrackCount

	// Extract primary video track info
	if len(videoTracks) > 0 {
		primary := videoTracks[0]
		if primary.Width != nil {
			trigger.PrimaryWidth = primary.Width
		}
		if primary.Height != nil {
			trigger.PrimaryHeight = primary.Height
		}
		if primary.Fps != nil {
			trigger.PrimaryFps = primary.Fps
		}
		if primary.BitrateKbps != nil {
			primaryVideoBitrate := *primary.BitrateKbps
			trigger.PrimaryVideoBitrate = &primaryVideoBitrate
		}
		if primary.Codec != "" {
			trigger.PrimaryVideoCodec = &primary.Codec
		}
	}

	// Extract primary audio track info
	if len(audioTracks) > 0 {
		primary := audioTracks[0]
		if primary.BitrateKbps != nil {
			primaryAudioBitrate := *primary.BitrateKbps
			trigger.PrimaryAudioBitrate = &primaryAudioBitrate
		}
		if primary.Codec != "" {
			trigger.PrimaryAudioCodec = &primary.Codec
		}
		if primary.Channels != nil {
			trigger.PrimaryAudioChannels = primary.Channels
		}
		if primary.SampleRate != nil {
			trigger.PrimaryAudioSampleRate = primary.SampleRate
		}
	}

	// Determine quality tier
	qualityTier := determineQualityTier(tracks)
	if qualityTier != "" {
		trigger.QualityTier = &qualityTier
	}
}

// determineQualityTier determines quality tier with rich format: "1080p60 H264 @ 6Mbps"
func determineQualityTier(tracks []*pb.StreamTrack) string {
	// Find primary video track (highest resolution)
	var primaryVideo *pb.StreamTrack
	maxHeight := int32(0)
	for _, track := range tracks {
		if track.TrackType == "video" && track.Height != nil {
			if *track.Height > maxHeight {
				maxHeight = *track.Height
				primaryVideo = track
			}
		}
	}

	if primaryVideo == nil || maxHeight == 0 {
		return ""
	}

	// Resolution tier
	var resolution string
	if maxHeight >= 2160 {
		resolution = "4K"
	} else if maxHeight >= 1440 {
		resolution = "1440p"
	} else if maxHeight >= 1080 {
		resolution = "1080p"
	} else if maxHeight >= 720 {
		resolution = "720p"
	} else if maxHeight >= 480 {
		resolution = "480p"
	} else {
		resolution = "SD"
	}

	// Append FPS if available (rounded to nearest integer)
	if primaryVideo.Fps != nil && *primaryVideo.Fps > 0 {
		fps := int(*primaryVideo.Fps + 0.5) // Round to nearest int
		resolution = fmt.Sprintf("%s%d", resolution, fps)
	}

	// Build rich label with available data
	parts := []string{resolution}

	// Add codec if available
	if primaryVideo.Codec != "" {
		parts = append(parts, primaryVideo.Codec)
	}

	// Add bitrate if available (prefer kbps, format nicely)
	if primaryVideo.BitrateKbps != nil && *primaryVideo.BitrateKbps > 0 {
		bitrate := *primaryVideo.BitrateKbps
		var bitrateStr string
		if bitrate >= 1000 {
			bitrateStr = fmt.Sprintf("%.1fMbps", float64(bitrate)/1000)
		} else {
			bitrateStr = fmt.Sprintf("%dkbps", bitrate)
		}
		parts = append(parts, "@", bitrateStr)
	} else if primaryVideo.BitrateBps != nil && *primaryVideo.BitrateBps > 0 {
		// Fallback to bps
		bitrate := float64(*primaryVideo.BitrateBps) / 1000 // Convert to kbps
		var bitrateStr string
		if bitrate >= 1000 {
			bitrateStr = fmt.Sprintf("%.1fMbps", bitrate/1000)
		} else {
			bitrateStr = fmt.Sprintf("%.0fkbps", bitrate)
		}
		parts = append(parts, "@", bitrateStr)
	}

	// Join parts: "1080p60 H264 @ 6.0Mbps"
	// Handle the "@" as a separator properly
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		if parts[i] == "@" {
			result += " @"
		} else if i > 0 && parts[i-1] == "@" {
			result += " " + parts[i]
		} else {
			result += " " + parts[i]
		}
	}

	return result
}
