package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	MistWebhookRequests        *prometheus.CounterVec
}

var (
	logger   logging.Logger
	metrics  *HandlerMetrics
	nodeName string
)

// VideoExtensions lists file extensions recognized as video container formats.
var VideoExtensions = []string{".mp4", ".webm", ".mkv", ".avi", ".ts", ".mov", ".m4v", ".flv"}

// IsVideoFile returns true if the extension is a recognized video container format.
func IsVideoFile(ext string) bool {
	switch ext {
	case ".mp4", ".webm", ".mkv", ".avi", ".ts", ".mov", ".m4v", ".flv":
		return true
	}
	return false
}

func incMistWebhook(triggerType, status string) {
	if metrics == nil || metrics.MistWebhookRequests == nil {
		return
	}
	metrics.MistWebhookRequests.WithLabelValues(triggerType, status).Inc()
}

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
	control.SetOnStorageWrite(TriggerStorageCheck)

	// Register delete handlers with control package (avoids import cycle)
	control.SetDeleteClipHandler(func(clipHash string) (uint64, error) {
		return Current().DeleteClip(clipHash)
	})
	control.SetDeleteDVRHandler(func(dvrHash string) (uint64, error) {
		return Current().DeleteDVR(dvrHash)
	})
	control.SetDeleteVodHandler(func(vodHash string) (uint64, error) {
		return Current().DeleteVOD(vodHash)
	})

	logger.WithField("node_name", nodeName).Info("Handlers initialized")
}

// Handlers provides access to handler operations for use by control package
type Handlers struct {
	storagePath string
}

var currentHandlers *Handlers

// Current returns the current handlers instance
func Current() *Handlers {
	if currentHandlers == nil {
		currentHandlers = &Handlers{storagePath: config.GetStoragePath()}
	}
	return currentHandlers
}

// DeleteClip deletes clip files from local storage
// Returns the total size of deleted files in bytes
func (h *Handlers) DeleteClip(clipHash string) (uint64, error) {
	if clipHash == "" {
		return 0, fmt.Errorf("clip hash is required")
	}

	clipsDir := filepath.Join(h.storagePath, "clips")
	var totalSize uint64

	// Pattern match: look for files starting with clipHash
	pattern := filepath.Join(clipsDir, clipHash+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("failed to glob clip files: %w", err)
	}

	// Also try nested directories (clips/{stream_name}/{clipHash}.*)
	altPattern := filepath.Join(clipsDir, "*", clipHash+"*")
	altMatches, _ := filepath.Glob(altPattern)
	matches = append(matches, altMatches...)

	if len(matches) == 0 {
		logger.WithField("clip_hash", clipHash).Debug("No clip files found to delete")
		return 0, nil // Not an error, just nothing to delete
	}

	for _, filePath := range matches {
		info, err := os.Stat(filePath)
		if err != nil {
			logger.WithError(err).WithField("file", filePath).Warn("Failed to stat clip file")
			continue
		}

		fileSize := uint64(info.Size())
		if err := os.Remove(filePath); err != nil {
			logger.WithError(err).WithField("file", filePath).Warn("Failed to remove clip file")
			continue
		}

		totalSize += fileSize
		logger.WithFields(logging.Fields{
			"clip_hash": clipHash,
			"file":      filePath,
			"size":      fileSize,
		}).Debug("Deleted clip file")
	}

	// Also remove any VOD symlinks (like cleanup.go does)
	// VOD symlinks may exist at clips/{clipHash}.{ext}
	for _, ext := range VideoExtensions {
		vodLinkPath := filepath.Join(clipsDir, clipHash+ext)
		if info, err := os.Lstat(vodLinkPath); err == nil {
			// Check if it's a symlink
			if info.Mode()&os.ModeSymlink != 0 {
				if err := os.Remove(vodLinkPath); err != nil {
					logger.WithError(err).WithField("vod_path", vodLinkPath).Warn("Failed to remove VOD symlink")
				} else {
					logger.WithField("vod_path", vodLinkPath).Debug("Removed VOD symlink")
				}
			}
		}
	}

	// Remove from artifact index if prometheus monitor is available
	if prometheusMonitor != nil {
		prometheusMonitor.mutex.Lock()
		delete(prometheusMonitor.artifactIndex, clipHash)
		prometheusMonitor.mutex.Unlock()
	}

	logger.WithFields(logging.Fields{
		"clip_hash":   clipHash,
		"total_size":  totalSize,
		"files_count": len(matches),
	}).Info("Clip files deleted")

	return totalSize, nil
}

// DeleteDVR deletes DVR recording files from local storage
// Returns the total size of deleted files in bytes
func (h *Handlers) DeleteDVR(dvrHash string) (uint64, error) {
	if dvrHash == "" {
		return 0, fmt.Errorf("DVR hash is required")
	}

	dvrDir := filepath.Join(h.storagePath, "dvr")

	// Find the manifest file: /dvr/{stream_id}/{dvr_hash}/{dvr_hash}.m3u8
	manifestPattern := filepath.Join(dvrDir, "*", dvrHash, dvrHash+".m3u8")
	manifestMatches, _ := filepath.Glob(manifestPattern)

	if len(manifestMatches) == 0 {
		logger.WithField("dvr_hash", dvrHash).Debug("No DVR files found to delete")
		return 0, nil
	}

	var totalSize uint64

	for _, manifestPath := range manifestMatches {
		// The recording directory is the parent of the manifest (the dvr_hash directory)
		recordingDir := filepath.Dir(manifestPath)

		// Delete the entire recording directory recursively
		size, err := h.deletePathRecursive(recordingDir)
		if err != nil {
			return totalSize, fmt.Errorf("failed to delete DVR directory: %w", err)
		}
		totalSize += size

		// Clean up empty stream_id directory if it's now empty
		streamDir := filepath.Dir(recordingDir)
		h.removeEmptyDir(streamDir)
	}

	logger.WithFields(logging.Fields{
		"dvr_hash":   dvrHash,
		"total_size": totalSize,
	}).Info("DVR recording deleted")

	return totalSize, nil
}

// DeleteVOD deletes VOD (uploaded) asset files from local storage
// Returns the total size of deleted files in bytes
func (h *Handlers) DeleteVOD(vodHash string) (uint64, error) {
	if vodHash == "" {
		return 0, fmt.Errorf("VOD hash is required")
	}

	vodDir := filepath.Join(h.storagePath, "vod")
	var totalSize uint64

	// VOD files are stored as: vod/{vodHash}.{ext}
	pattern := filepath.Join(vodDir, vodHash+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("failed to glob VOD files: %w", err)
	}

	if len(matches) == 0 {
		logger.WithField("vod_hash", vodHash).Debug("No VOD files found to delete")
		return 0, nil
	}

	for _, filePath := range matches {
		info, err := os.Stat(filePath)
		if err != nil {
			logger.WithError(err).WithField("file", filePath).Warn("Failed to stat VOD file")
			continue
		}

		fileSize := uint64(info.Size())
		if err := os.Remove(filePath); err != nil {
			logger.WithError(err).WithField("file", filePath).Warn("Failed to remove VOD file")
			continue
		}

		totalSize += fileSize
		logger.WithFields(logging.Fields{
			"vod_hash": vodHash,
			"file":     filePath,
			"size":     fileSize,
		}).Debug("Deleted VOD file")
	}

	// Remove from artifact index if prometheus monitor is available
	if prometheusMonitor != nil {
		prometheusMonitor.mutex.Lock()
		delete(prometheusMonitor.artifactIndex, vodHash)
		prometheusMonitor.mutex.Unlock()
	}

	logger.WithFields(logging.Fields{
		"vod_hash":    vodHash,
		"total_size":  totalSize,
		"files_count": len(matches),
	}).Info("VOD asset deleted")

	return totalSize, nil
}

// parseManifestSegments reads an HLS manifest and extracts segment file paths
func (h *Handlers) parseManifestSegments(manifestPath, baseDir string) []string {
	var segments []string

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		logger.WithError(err).WithField("path", manifestPath).Warn("Failed to read manifest for segment parsing")
		return segments
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip empty lines and tags
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// This is a segment reference (relative path like "segments/1234_0.ts")
		segPath := filepath.Join(baseDir, line)
		segments = append(segments, segPath)
	}

	return segments
}

// removeEmptyDir removes a directory only if it's empty
func (h *Handlers) removeEmptyDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if len(entries) == 0 {
		os.Remove(dir)
	}
}

// deletePathRecursive recursively deletes a path and returns total bytes deleted
func (h *Handlers) deletePathRecursive(path string) (uint64, error) {
	var totalSize uint64

	err := filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			//nolint:nilerr // skip errors, continue walking
			return nil
		}
		if !info.IsDir() {
			totalSize += uint64(info.Size())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	if err := os.RemoveAll(path); err != nil {
		return 0, err
	}

	return totalSize, nil
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

// HandlePushRewrite handles the PUSH_REWRITE trigger from MistServer
// This is a critical blocking trigger - validates stream keys and routes to wildcard streams
func HandlePushRewrite(c *gin.Context) {
	start := time.Now()
	incMistWebhook("PUSH_REWRITE", "received")

	// Track node operations
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("push_rewrite", "requested").Inc()
	}

	// Read the raw body - MistServer sends parameters as raw text, not JSON
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("PUSH_REWRITE", "read_error")
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
		incMistWebhook("PUSH_REWRITE", "parse_error")
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
		incMistWebhook("PUSH_REWRITE", "forward_error")
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
		incMistWebhook("PUSH_REWRITE", "aborted")
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
	incMistWebhook("PUSH_REWRITE", "success")

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
	incMistWebhook("PLAY_REWRITE", "received")

	// Track node operations
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("play_rewrite", "requested").Inc()
	}

	// Read the raw body - MistServer sends parameters as raw text, not JSON
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("PLAY_REWRITE", "read_error")
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
		"payload_raw":  string(body),
	}).Debug("Forwarding PLAY_REWRITE trigger to Foghorn via gRPC")

	// Parse raw webhook data directly
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerPlayRewrite, body, control.GetCurrentNodeID(), logger)
	if err != nil {
		incMistWebhook("PLAY_REWRITE", "parse_error")
		logger.WithFields(logging.Fields{
			"error":       err,
			"payload_raw": string(body),
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
		incMistWebhook("PLAY_REWRITE", "forward_error")
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
		incMistWebhook("PLAY_REWRITE", "aborted")
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
	incMistWebhook("PLAY_REWRITE", "success")

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
	incMistWebhook("STREAM_SOURCE", "received")

	// Track node operations
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("stream_source", "requested").Inc()
	}

	// Read the raw body - MistServer sends parameters as raw text, not JSON
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("STREAM_SOURCE", "read_error")
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
		incMistWebhook("STREAM_SOURCE", "parse_error")
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
		incMistWebhook("STREAM_SOURCE", "forward_error")
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
		incMistWebhook("STREAM_SOURCE", "aborted")
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
	incMistWebhook("STREAM_SOURCE", "success")

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

func extractVODHash(streamName string) string {
	if streamName == "" {
		return ""
	}
	if strings.HasPrefix(streamName, "vod+") {
		return strings.TrimPrefix(streamName, "vod+")
	}
	if len(streamName) == 32 {
		return streamName
	}
	return ""
}

// HandlePushEnd handles PUSH_END webhook
// This is a non-blocking trigger that logs push completion status
func HandlePushEnd(c *gin.Context) {
	incMistWebhook("PUSH_END", "received")
	// Track infrastructure event
	if metrics != nil {
		metrics.InfrastructureEvents.WithLabelValues("push_end").Inc()
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("PUSH_END", "read_error")
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
		incMistWebhook("PUSH_END", "parse_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse PUSH_END trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		incMistWebhook("PUSH_END", "forward_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward PUSH_END to Foghorn")
	} else {
		incMistWebhook("PUSH_END", "success")
	}

	c.String(http.StatusOK, "OK")
}

// HandlePushOutStart handles PUSH_OUT_START webhook
// This is a blocking trigger - validates and routes outbound pushes
func HandlePushOutStart(c *gin.Context) {
	incMistWebhook("PUSH_OUT_START", "received")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("PUSH_OUT_START", "read_error")
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
		incMistWebhook("PUSH_OUT_START", "parse_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse PUSH_OUT_START trigger")
		c.String(http.StatusOK, "") // Empty response aborts push
		return
	}

	// Forward trigger to Foghorn via gRPC and get response
	response, shouldAbort, err := control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		incMistWebhook("PUSH_OUT_START", "forward_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward PUSH_OUT_START to Foghorn")
		c.String(http.StatusOK, "") // Empty response aborts push
		return
	}

	if shouldAbort {
		incMistWebhook("PUSH_OUT_START", "aborted")
		logger.WithFields(logging.Fields{
			"response": response,
		}).Info("PUSH_OUT_START aborted by Foghorn")
		c.String(http.StatusOK, "") // Empty response aborts push
		return
	}

	logger.WithFields(logging.Fields{
		"response": response,
	}).Info("PUSH_OUT_START approved by Foghorn")
	incMistWebhook("PUSH_OUT_START", "success")

	// Return Foghorn's response to MistServer
	c.String(http.StatusOK, response)
}

// HandleStreamBuffer handles STREAM_BUFFER webhook
// This is a non-blocking trigger that monitors stream buffer state and health
func HandleStreamBuffer(c *gin.Context) {
	incMistWebhook("STREAM_BUFFER", "received")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("STREAM_BUFFER", "read_error")
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
		incMistWebhook("STREAM_BUFFER", "parse_error")
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
		incMistWebhook("STREAM_BUFFER", "forward_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward STREAM_BUFFER to Foghorn")
	} else {
		incMistWebhook("STREAM_BUFFER", "success")
	}

	c.String(http.StatusOK, "OK")
}

// HandleStreamEnd handles STREAM_END webhook
// This is a non-blocking trigger that reports stream end metrics
func HandleStreamEnd(c *gin.Context) {
	incMistWebhook("STREAM_END", "received")
	// Track infrastructure event
	if metrics != nil {
		metrics.InfrastructureEvents.WithLabelValues("stream_end").Inc()
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("STREAM_END", "read_error")
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
		incMistWebhook("STREAM_END", "parse_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse STREAM_END trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		incMistWebhook("STREAM_END", "forward_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward STREAM_END to Foghorn")
	} else {
		incMistWebhook("STREAM_END", "success")
	}

	c.String(http.StatusOK, "OK")
}

// HandleUserNew handles USER_NEW webhook
// This is a blocking trigger that validates new viewer connections
func HandleUserNew(c *gin.Context) {
	incMistWebhook("USER_NEW", "received")
	// Track infrastructure event
	if metrics != nil {
		metrics.InfrastructureEvents.WithLabelValues("user_connected").Inc()
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("USER_NEW", "read_error")
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
		incMistWebhook("USER_NEW", "parse_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse USER_NEW trigger")
		c.String(http.StatusOK, "false") // Deny session on error
		return
	}

	// Forward trigger to Foghorn via gRPC and get response
	response, shouldAbort, err := control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		incMistWebhook("USER_NEW", "forward_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward USER_NEW to Foghorn")
		c.String(http.StatusOK, "false") // Deny session on error
		return
	}

	if shouldAbort {
		incMistWebhook("USER_NEW", "aborted")
		logger.WithFields(logging.Fields{
			"response": response,
		}).Info("USER_NEW denied by Foghorn")
		c.String(http.StatusOK, "false") // Deny session
		return
	}

	logger.WithFields(logging.Fields{
		"response": response,
	}).Info("USER_NEW approved by Foghorn")
	incMistWebhook("USER_NEW", "success")

	// Track VOD/clip/DVR access on viewer connect (canonical trigger).
	if mt := mistTrigger.GetViewerConnect(); mt != nil {
		if hash := extractVODHash(mt.GetStreamName()); hash != "" {
			touchArtifactAccess(hash)
		}
	}

	// Return Foghorn's response to MistServer
	c.String(http.StatusOK, response)
}

// HandleUserEnd handles USER_END webhook
// This is a non-blocking trigger that reports viewer disconnection metrics
func HandleUserEnd(c *gin.Context) {
	incMistWebhook("USER_END", "received")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("USER_END", "read_error")
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
		incMistWebhook("USER_END", "parse_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse USER_END trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		incMistWebhook("USER_END", "forward_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward USER_END to Foghorn")
	} else {
		incMistWebhook("USER_END", "success")
	}

	c.String(http.StatusOK, "OK")
}

// HandleLiveTrackList handles LIVE_TRACK_LIST webhook
// Payload: stream name, track list (JSON)
func HandleLiveTrackList(c *gin.Context) {
	incMistWebhook("LIVE_TRACK_LIST", "received")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("LIVE_TRACK_LIST", "read_error")
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
		incMistWebhook("LIVE_TRACK_LIST", "parse_error")
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
		incMistWebhook("LIVE_TRACK_LIST", "forward_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward LIVE_TRACK_LIST to Foghorn")
	} else {
		incMistWebhook("LIVE_TRACK_LIST", "success")
	}

	c.String(http.StatusOK, "OK")
}

// HandleRecordingEnd handles RECORDING_END webhook
// CORRECT MistServer format: stream name, path to file, output protocol name, bytes written, seconds writing took, unix start time, unix end time, media duration (ms)
func HandleRecordingEnd(c *gin.Context) {
	incMistWebhook("RECORDING_END", "received")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("RECORDING_END", "read_error")
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
		incMistWebhook("RECORDING_END", "parse_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to parse RECORDING_END trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward trigger to Foghorn via gRPC (non-blocking)
	_, _, err = control.SendMistTrigger(mistTrigger, logger)
	if err != nil {
		incMistWebhook("RECORDING_END", "forward_error")
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to forward RECORDING_END to Foghorn")
	} else {
		incMistWebhook("RECORDING_END", "success")
	}

	// Dual-storage: Trigger immediate sync to S3 for new clips
	// This ensures clips are backed up to S3 immediately after creation
	if rec := mistTrigger.GetRecordingComplete(); rec != nil && rec.FilePath != "" {
		go triggerClipSync(rec.FilePath, uint64(rec.BytesWritten))
	}
	TriggerStorageCheck()

	c.String(http.StatusOK, "OK")
}

// HandleRecordingSegment handles RECORDING_SEGMENT webhook
// Used for immediate DVR segment syncing
func HandleRecordingSegment(c *gin.Context) {
	incMistWebhook("RECORDING_SEGMENT", "received")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("RECORDING_SEGMENT", "read_error")
		logger.WithError(err).Error("Failed to read RECORDING_SEGMENT body")
		c.String(http.StatusOK, "OK")
		return
	}

	// Parse trigger
	mistTrigger, err := mist.ParseTriggerToProtobuf(mist.TriggerRecordingSegment, body, getNodeID(), logger)
	if err != nil {
		incMistWebhook("RECORDING_SEGMENT", "parse_error")
		logger.WithError(err).Error("Failed to parse RECORDING_SEGMENT trigger")
		c.String(http.StatusOK, "OK")
		return
	}

	// Forward to Foghorn for analytics (fire-and-forget with error logging)
	go func() {
		if _, _, err := control.SendMistTrigger(mistTrigger, logger); err != nil {
			incMistWebhook("RECORDING_SEGMENT", "forward_error")
			logger.WithError(err).WithField("trigger_type", "RECORDING_SEGMENT").Error("Failed to send RECORDING_SEGMENT trigger to Foghorn")
		} else {
			incMistWebhook("RECORDING_SEGMENT", "success")
		}
	}()

	// Trigger immediate sync in DVR Manager
	// Note: DVR billing is storage-based (via storage_snapshot), not process-based
	if seg := mistTrigger.GetRecordingSegment(); seg != nil {
		if dvrMgr := control.GetDVRManager(); dvrMgr != nil {
			dvrMgr.HandleNewSegment(seg.StreamName, seg.FilePath)
		}
	}
	TriggerStorageCheck()

	c.String(http.StatusOK, "OK")
}

// triggerClipSync initiates a background sync of a newly created clip to S3
func triggerClipSync(filePath string, sizeBytes uint64) {
	sm := GetStorageManager()
	if sm == nil {
		logger.Debug("Storage manager not initialized, skipping clip sync")
		return
	}

	// Extract clip hash from file path (filename without extension)
	filename := filepath.Base(filePath)
	ext := filepath.Ext(filename)
	clipHash := filename[:len(filename)-len(ext)]

	if clipHash == "" || len(clipHash) < 18 {
		logger.WithField("file_path", filePath).Debug("Invalid clip hash, skipping sync")
		return
	}

	logger.WithFields(logging.Fields{
		"clip_hash": clipHash,
		"file_path": filePath,
		"size_mb":   float64(sizeBytes) / (1024 * 1024),
	}).Info("Triggering immediate sync for new clip")

	// Create a freeze candidate and trigger sync
	candidate := FreezeCandidate{
		AssetType: AssetTypeClip,
		AssetHash: clipHash,
		FilePath:  filePath,
		SizeBytes: sizeBytes,
		CreatedAt: time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := sm.freezeAsset(ctx, candidate); err != nil {
		logger.WithError(err).WithField("clip_hash", clipHash).Error("Failed to sync clip to S3")
	}
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
		resolution = "2160p"
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

// HandleLivepeerSegmentComplete handles LIVEPEER_SEGMENT_COMPLETE webhook
// This is a non-blocking trigger that reports Livepeer transcoding segment completion for billing
// Payload (15 fields):
//  0. stream name, 1. livepeer session ID, 2. segment number, 3. segment start ms,
//  4. segment duration ms, 5. source width, 6. source height, 7. input bytes,
//  8. output bytes total, 9. rendition count, 10. attempt count, 11. broadcaster URL,
//  12. turnaround ms, 13. speed factor, 14. renditions JSON
func HandleLivepeerSegmentComplete(c *gin.Context) {
	incMistWebhook("LIVEPEER_SEGMENT_COMPLETE", "received")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("LIVEPEER_SEGMENT_COMPLETE", "read_error")
		logger.WithError(err).Error("Failed to read LIVEPEER_SEGMENT_COMPLETE body")
		c.String(http.StatusOK, "OK")
		return
	}

	payloadStr := strings.TrimSpace(string(body))
	params := strings.Split(payloadStr, "\n")
	if len(params) < 15 {
		incMistWebhook("LIVEPEER_SEGMENT_COMPLETE", "parse_error")
		logger.WithFields(logging.Fields{
			"payload":     payloadStr,
			"param_count": len(params),
		}).Warn("LIVEPEER_SEGMENT_COMPLETE incomplete payload")
		c.String(http.StatusOK, "OK")
		return
	}

	streamName := params[0]
	livepeerSessionId := params[1]
	segmentNum := params[2]
	segmentStartMs := params[3]
	durationMs := params[4]
	width := params[5]
	height := params[6]
	inputBytes := params[7]
	outputBytesTotal := params[8]
	renditionCount := params[9]
	attemptCount := params[10]
	broadcasterURL := params[11]
	turnaroundMs := params[12]
	speedFactor := params[13]
	renditionsJson := params[14]

	logger.WithFields(logging.Fields{
		"trigger_type":        "LIVEPEER_SEGMENT_COMPLETE",
		"stream_name":         streamName,
		"livepeer_session_id": livepeerSessionId,
		"segment_num":         segmentNum,
		"duration_ms":         durationMs,
		"resolution":          width + "x" + height,
		"rendition_count":     renditionCount,
		"turnaround_ms":       turnaroundMs,
		"speed_factor":        speedFactor,
	}).Info("Livepeer segment transcoded")

	if metrics != nil {
		metrics.InfrastructureEvents.WithLabelValues("livepeer_segment_complete").Inc()
	}

	durationMsInt, _ := strconv.ParseInt(durationMs, 10, 64)
	segmentNumInt, _ := strconv.Atoi(segmentNum)
	segmentStartMsInt, _ := strconv.ParseInt(segmentStartMs, 10, 64)
	widthInt, _ := strconv.Atoi(width)
	heightInt, _ := strconv.Atoi(height)
	inputBytesInt, _ := strconv.ParseInt(inputBytes, 10, 64)
	outputBytesTotalInt, _ := strconv.ParseInt(outputBytesTotal, 10, 64)
	renditionCountInt, _ := strconv.Atoi(renditionCount)
	attemptCountInt, _ := strconv.Atoi(attemptCount)
	turnaroundMsInt, _ := strconv.ParseInt(turnaroundMs, 10, 64)
	speedFactorFloat, _ := strconv.ParseFloat(speedFactor, 64)

	billingEvent := &pb.ProcessBillingEvent{
		NodeId:            nodeName,
		StreamName:        streamName,
		ProcessType:       "Livepeer",
		DurationMs:        durationMsInt,
		Timestamp:         time.Now().Unix(),
		TenantId:          stringPtr(config.GetTenantID()),
		LivepeerSessionId: stringPtr(livepeerSessionId),
		SegmentNumber:     int32Ptr(int32(segmentNumInt)),
		SegmentStartMs:    int64Ptr(segmentStartMsInt),
		Width:             int32Ptr(int32(widthInt)),
		Height:            int32Ptr(int32(heightInt)),
		InputBytes:        int64Ptr(inputBytesInt),
		OutputBytesTotal:  int64Ptr(outputBytesTotalInt),
		RenditionCount:    int32Ptr(int32(renditionCountInt)),
		AttemptCount:      int32Ptr(int32(attemptCountInt)),
		BroadcasterUrl:    stringPtr(broadcasterURL),
		TurnaroundMs:      int64Ptr(turnaroundMsInt),
		SpeedFactor:       float64Ptr(speedFactorFloat),
		RenditionsJson:    stringPtr(renditionsJson),
	}

	if err := control.SendProcessBillingEvent(billingEvent); err != nil {
		incMistWebhook("LIVEPEER_SEGMENT_COMPLETE", "forward_error")
		logger.WithError(err).Warn("Failed to send Livepeer billing event to Foghorn")
	} else {
		incMistWebhook("LIVEPEER_SEGMENT_COMPLETE", "success")
	}

	c.String(http.StatusOK, "OK")
}

// HandleProcessAVSegmentComplete handles PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE webhook
// This is a non-blocking trigger that reports MistProcAV transcoding progress for billing
// Fires every 5 seconds during operation AND once on exit (is_final=1)
// Payload (31 fields):
//  0. stream name, 1. track type, 2. seconds since last, 3. input frames cumulative,
//  4. output frames cumulative, 5. input frames delta, 6. output frames delta,
//  7. input bytes delta, 8. output bytes delta, 9. decode us/frame, 10. transform us/frame,
//  11. encode us/frame, 12. input codec, 13. output codec, 14. input width, 15. input height,
//  16. output width, 17. output height, 18. input fpks, 19. output fps, 20. sample rate,
//  21. channels, 22. source timestamp ms, 23. sink timestamp ms, 24. source advanced ms,
//  25. sink advanced ms, 26. rtf in, 27. rtf out, 28. pipeline lag ms, 29. output bitrate bps,
//  30. is_final
func HandleProcessAVSegmentComplete(c *gin.Context) {
	incMistWebhook("PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", "received")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", "read_error")
		logger.WithError(err).Error("Failed to read PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE body")
		c.String(http.StatusOK, "OK")
		return
	}

	payloadStr := strings.TrimSpace(string(body))
	params := strings.Split(payloadStr, "\n")
	if len(params) < 31 {
		incMistWebhook("PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", "parse_error")
		logger.WithFields(logging.Fields{
			"payload":     payloadStr,
			"param_count": len(params),
		}).Warn("PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE incomplete payload")
		c.String(http.StatusOK, "OK")
		return
	}

	streamName := params[0]
	trackType := params[1]
	secondsSinceLast := params[2]
	inputFrames := params[3]
	outputFrames := params[4]
	inputFramesDelta := params[5]
	outputFramesDelta := params[6]
	inputBytesDelta := params[7]
	outputBytesDelta := params[8]
	decodeUs := params[9]
	transformUs := params[10]
	encodeUs := params[11]
	inputCodec := params[12]
	outputCodec := params[13]
	inputWidth := params[14]
	inputHeight := params[15]
	outputWidth := params[16]
	outputHeight := params[17]
	inputFpks := params[18]
	outputFps := params[19]
	sampleRate := params[20]
	channels := params[21]
	sourceTimestampMs := params[22]
	sinkTimestampMs := params[23]
	sourceAdvancedMs := params[24]
	sinkAdvancedMs := params[25]
	rtfIn := params[26]
	rtfOut := params[27]
	pipelineLagMs := params[28]
	outputBitrateBps := params[29]
	isFinal := params[30]

	logger.WithFields(logging.Fields{
		"trigger_type":       "PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE",
		"stream_name":        streamName,
		"track_type":         trackType,
		"seconds_since_last": secondsSinceLast,
		"input_codec":        inputCodec,
		"output_codec":       outputCodec,
		"resolution_in":      inputWidth + "x" + inputHeight,
		"resolution_out":     outputWidth + "x" + outputHeight,
		"rtf_out":            rtfOut,
		"is_final":           isFinal,
	}).Info("MistProcAV segment processed")

	if metrics != nil {
		if isFinal == "1" {
			metrics.InfrastructureEvents.WithLabelValues("process_av_complete").Inc()
		} else {
			metrics.InfrastructureEvents.WithLabelValues("process_av_segment").Inc()
		}
	}

	secondsSinceLastInt, _ := strconv.ParseInt(secondsSinceLast, 10, 64)
	inputFramesInt, _ := strconv.ParseInt(inputFrames, 10, 64)
	outputFramesInt, _ := strconv.ParseInt(outputFrames, 10, 64)
	inputFramesDeltaInt, _ := strconv.ParseInt(inputFramesDelta, 10, 64)
	outputFramesDeltaInt, _ := strconv.ParseInt(outputFramesDelta, 10, 64)
	inputBytesDeltaInt, _ := strconv.ParseInt(inputBytesDelta, 10, 64)
	outputBytesDeltaInt, _ := strconv.ParseInt(outputBytesDelta, 10, 64)
	decodeUsInt, _ := strconv.ParseInt(decodeUs, 10, 64)
	transformUsInt, _ := strconv.ParseInt(transformUs, 10, 64)
	encodeUsInt, _ := strconv.ParseInt(encodeUs, 10, 64)
	inputWidthInt, _ := strconv.Atoi(inputWidth)
	inputHeightInt, _ := strconv.Atoi(inputHeight)
	outputWidthInt, _ := strconv.Atoi(outputWidth)
	outputHeightInt, _ := strconv.Atoi(outputHeight)
	inputFpksInt, _ := strconv.Atoi(inputFpks)
	outputFpsFloat, _ := strconv.ParseFloat(outputFps, 64)
	sampleRateInt, _ := strconv.Atoi(sampleRate)
	channelsInt, _ := strconv.Atoi(channels)
	sourceTimestampMsInt, _ := strconv.ParseInt(sourceTimestampMs, 10, 64)
	sinkTimestampMsInt, _ := strconv.ParseInt(sinkTimestampMs, 10, 64)
	sourceAdvancedMsInt, _ := strconv.ParseInt(sourceAdvancedMs, 10, 64)
	sinkAdvancedMsInt, _ := strconv.ParseInt(sinkAdvancedMs, 10, 64)
	rtfInFloat, _ := strconv.ParseFloat(rtfIn, 64)
	rtfOutFloat, _ := strconv.ParseFloat(rtfOut, 64)
	pipelineLagMsInt, _ := strconv.ParseInt(pipelineLagMs, 10, 64)
	outputBitrateBpsInt, _ := strconv.ParseInt(outputBitrateBps, 10, 64)
	isFinalBool := isFinal == "1"

	durationMs := secondsSinceLastInt * 1000

	billingEvent := &pb.ProcessBillingEvent{
		NodeId:              nodeName,
		StreamName:          streamName,
		ProcessType:         "AV",
		DurationMs:          durationMs,
		Timestamp:           time.Now().Unix(),
		TenantId:            stringPtr(config.GetTenantID()),
		TrackType:           stringPtr(trackType),
		InputFrames:         int64Ptr(inputFramesInt),
		OutputFrames:        int64Ptr(outputFramesInt),
		InputFramesDelta:    int64Ptr(inputFramesDeltaInt),
		OutputFramesDelta:   int64Ptr(outputFramesDeltaInt),
		InputBytesDelta:     int64Ptr(inputBytesDeltaInt),
		OutputBytesDelta:    int64Ptr(outputBytesDeltaInt),
		DecodeUsPerFrame:    int64Ptr(decodeUsInt),
		TransformUsPerFrame: int64Ptr(transformUsInt),
		EncodeUsPerFrame:    int64Ptr(encodeUsInt),
		InputCodec:          stringPtr(inputCodec),
		OutputCodec:         stringPtr(outputCodec),
		InputWidth:          int32Ptr(int32(inputWidthInt)),
		InputHeight:         int32Ptr(int32(inputHeightInt)),
		OutputWidth:         int32Ptr(int32(outputWidthInt)),
		OutputHeight:        int32Ptr(int32(outputHeightInt)),
		InputFpks:           int32Ptr(int32(inputFpksInt)),
		OutputFpsMeasured:   float64Ptr(outputFpsFloat),
		SampleRate:          int32Ptr(int32(sampleRateInt)),
		Channels:            int32Ptr(int32(channelsInt)),
		SourceTimestampMs:   int64Ptr(sourceTimestampMsInt),
		SinkTimestampMs:     int64Ptr(sinkTimestampMsInt),
		SourceAdvancedMs:    int64Ptr(sourceAdvancedMsInt),
		SinkAdvancedMs:      int64Ptr(sinkAdvancedMsInt),
		RtfIn:               float64Ptr(rtfInFloat),
		RtfOut:              float64Ptr(rtfOutFloat),
		PipelineLagMs:       int64Ptr(pipelineLagMsInt),
		OutputBitrateBps:    int64Ptr(outputBitrateBpsInt),
		IsFinal:             boolPtr(isFinalBool),
	}

	if err := control.SendProcessBillingEvent(billingEvent); err != nil {
		incMistWebhook("PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", "forward_error")
		logger.WithError(err).Warn("Failed to send MistProcAV billing event to Foghorn")
	} else {
		incMistWebhook("PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", "success")
	}

	c.String(http.StatusOK, "OK")
}

// Helper functions for optional proto fields
func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func int32Ptr(i int32) *int32 {
	return &i
}

func int64Ptr(i int64) *int64 {
	return &i
}

func boolPtr(b bool) *bool {
	return &b
}

func float64Ptr(f float64) *float64 {
	return &f
}
