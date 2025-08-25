package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"frameworks/api_sidecar/internal/control"
	commodoreapi "frameworks/pkg/api/commodore"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/foghorn"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/validation"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// HandlerMetrics holds the metrics for handler operations
type HandlerMetrics struct {
	NodeOperations             *prometheus.CounterVec
	InfrastructureEvents       *prometheus.CounterVec
	NodeHealthChecks           *prometheus.CounterVec
	ResourceAllocationDuration *prometheus.HistogramVec
}

var (
	logger          logging.Logger
	metrics         *HandlerMetrics
	apiBaseURL      string
	clusterID       string
	foghornURI      string
	nodeName        string
	serviceToken    string
	commodoreClient *commodore.Client
	foghornClient   *foghorn.Client
	geoipReader     *geoip.Reader
)

// Init initializes the handlers with logger, metrics, and service URLs and cluster metadata
func Init(log logging.Logger, m *HandlerMetrics) {
	logger = log
	metrics = m

	apiBaseURL = os.Getenv("COMMODORE_URL")
	if apiBaseURL == "" {
		apiBaseURL = "http://localhost:18001"
	}

	clusterID = os.Getenv("CLUSTER_ID")
	if clusterID == "" {
		clusterID = "local-cluster"
	}

	foghornURI = os.Getenv("FOGHORN_URL")
	if foghornURI == "" {
		foghornURI = "http://localhost:18008"
	}

	nodeName = os.Getenv("NODE_NAME")
	if nodeName == "" {
		nodeName = "local-mistserver"
	}

	serviceToken = os.Getenv("SERVICE_TOKEN")

	// Initialize Commodore client
	commodoreClient = commodore.NewClient(commodore.Config{
		BaseURL:      apiBaseURL,
		ServiceToken: serviceToken,
		Timeout:      10 * time.Second,
		Logger:       logger,
	})

	foghornClient = foghorn.NewClient(foghorn.Config{
		BaseURL:      foghornURI,
		ServiceToken: serviceToken,
		Timeout:      10 * time.Second,
		Logger:       logger,
	})

	// Initialize GeoIP reader
	geoipPath := os.Getenv("GEOIP_MMDB_PATH")
	if geoipPath != "" {
		reader, err := geoip.NewReader(geoipPath)
		if err != nil {
			logger.WithFields(logging.Fields{
				"geoip_path": geoipPath,
				"error":      err,
			}).Warn("Failed to initialize GeoIP reader, geo enrichment disabled")
		} else {
			geoipReader = reader
			logger.WithField("geoip_path", geoipPath).Info("GeoIP reader initialized successfully")
		}
	} else {
		logger.Debug("No GEOIP_MMDB_PATH provided, geo enrichment disabled")
	}

	// Initialize the Decklog client for analytics forwarding
	InitDecklogClient()

	// Initialize Prometheus monitoring
	InitPrometheusMonitor(logger)

	logger.WithFields(logging.Fields{
		"commodore_url": apiBaseURL,
		"cluster_id":    clusterID,
		"foghorn_uri":   foghornURI,
		"node_name":     nodeName,
		"geoip_enabled": geoipReader != nil,
	}).Info("Handlers initialized")
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

func validateStreamKeyViaAPI(streamKey string) (*commodoreapi.ValidateStreamKeyResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return commodoreClient.ValidateStreamKey(ctx, streamKey)
}

// forwardEventToCommodore forwards stream events to Commodore for processing
func forwardEventToCommodore(endpoint string, eventData map[string]interface{}) error {
	enrichedData := enrichEventWithClusterMetadata(eventData)

	jsonData, err := json.Marshal(enrichedData)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

	url := fmt.Sprintf("%s/%s", apiBaseURL, endpoint)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+serviceToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to forward event to Commodore: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		logger.WithFields(logging.Fields{
			"status_code": resp.StatusCode,
			"response":    string(body),
		}).Error("Commodore returned error")
	}

	return nil
}

// HandlePushRewrite handles the PUSH_REWRITE trigger from MistServer
// This is a critical trigger - validates stream keys and routes to wildcard streams
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

	// Parse the parameters - MistServer sends them as newline-separated text
	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 3 {
		logger.WithFields(logging.Fields{
			"error": "Invalid PUSH_REWRITE payload: expected 3 parameters, got " + fmt.Sprintf("%d", len(params)),
		}).Error("Invalid PUSH_REWRITE payload")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("push_rewrite", "invalid_payload").Inc()
		}

		c.String(http.StatusOK, "") // Empty response denies the push
		return
	}

	pushURL := params[0]
	hostname := params[1]
	streamName := params[2]

	logger.WithFields(logging.Fields{
		"push_url":    pushURL,
		"hostname":    hostname,
		"stream_name": streamName,
	}).Info("Received PUSH_REWRITE")

	// Extract stream key from the stream name
	streamKey := streamName

	// Validate stream key via Commodore API
	streamKeyValidation, err := validateStreamKeyViaAPI(streamKey)
	if err != nil {
		logger.WithFields(logging.Fields{
			"stream_key": streamKey,
			"error":      err,
		}).Error("Failed to validate stream key via API")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("push_rewrite", "validation_error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response denies the push
		return
	}

	if !streamKeyValidation.Valid {
		logger.WithFields(logging.Fields{
			"stream_key": streamKey,
			"api_error":  streamKeyValidation.Error,
		}).Error("Invalid stream key")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("push_rewrite", "invalid_key").Inc()
		}

		c.String(http.StatusOK, "") // Empty response denies the push
		return
	}

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Extract actual protocol from push URL instead of hardcoding
	protocol := "unknown"
	if strings.HasPrefix(pushURL, "rtmp://") {
		protocol = "rtmp"
	} else if strings.HasPrefix(pushURL, "srt://") {
		protocol = "srt"
	} else if strings.HasPrefix(pushURL, "whip://") {
		protocol = "whip"
	} else if strings.HasPrefix(pushURL, "http://") || strings.HasPrefix(pushURL, "https://") {
		protocol = "http"
	}

	// Forward stream start event to API for database updates
	go forwardEventToCommodore("stream-start", map[string]interface{}{
		"node_id":       nodeID,
		"stream_key":    streamKey,
		"internal_name": streamKeyValidation.InternalName,
		"hostname":      hostname,
		"push_url":      pushURL,
		"event_type":    "push_rewrite_success",
		"timestamp":     time.Now().Unix(),
	})

	// Create typed StreamIngestPayload
	ingestPayload := &validation.StreamIngestPayload{
		StreamKey:    streamKey,
		InternalName: streamKeyValidation.InternalName,
		UserID:       streamKeyValidation.UserID,
		NodeID:       nodeID,
		TenantID:     streamKeyValidation.TenantID,
		Hostname:     hostname,
		PushURL:      pushURL,
		Protocol:     protocol,
	}

	// Add geographic data from node location if available
	if prometheusMonitor != nil {
		prometheusMonitor.mutex.RLock()
		if prometheusMonitor.latitude != nil {
			ingestPayload.Latitude = *prometheusMonitor.latitude
		}
		if prometheusMonitor.longitude != nil {
			ingestPayload.Longitude = *prometheusMonitor.longitude
		}
		if prometheusMonitor.location != "" {
			ingestPayload.Location = prometheusMonitor.location
		}
		prometheusMonitor.mutex.RUnlock()
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:       uuid.New().String(),
		EventType:     validation.EventStreamIngest,
		Timestamp:     time.Now().UTC(),
		Source:        "mistserver_webhook",
		InternalName:  &streamKeyValidation.InternalName,
		UserID:        &streamKeyValidation.UserID,
		SchemaVersion: "2.0",
		StreamIngest:  ingestPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	// Check if DVR recording is enabled for this stream
	if streamKeyValidation.Recording != nil && streamKeyValidation.Recording.Enabled {
		logger.WithFields(logging.Fields{
			"internal_name":    streamKeyValidation.InternalName,
			"retention_days":   streamKeyValidation.Recording.RetentionDays,
			"format":           streamKeyValidation.Recording.Format,
			"segment_duration": streamKeyValidation.Recording.SegmentDuration,
		}).Info("DVR recording enabled for stream, requesting DVR start")

		// Start DVR recording via Foghorn
		go requestDVRStart(streamKeyValidation)
	}

	// Create wildcard stream name for MistServer routing
	wildcardStreamName := fmt.Sprintf("live+%s", streamKeyValidation.InternalName)

	logger.WithFields(logging.Fields{
		"stream_key":           streamKey,
		"wildcard_stream_name": wildcardStreamName,
		"user_id":              streamKeyValidation.UserID,
	}).Info("Stream key validated, routing to wildcard stream")

	// Track successful operation and resource allocation duration
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("push_rewrite", "success").Inc()
		metrics.ResourceAllocationDuration.WithLabelValues("stream_allocation").Observe(time.Since(start).Seconds())
		metrics.InfrastructureEvents.WithLabelValues("stream_allocated").Inc()
	}

	// Return the wildcard stream name for MistServer to use
	c.String(http.StatusOK, wildcardStreamName)
}

// getClipFromArtifactIndex performs fast O(1) lookup in the artifact index
func getClipFromArtifactIndex(clipHash string) *ClipInfo {
	if prometheusMonitor == nil {
		return nil
	}
	prometheusMonitor.mutex.RLock()
	defer prometheusMonitor.mutex.RUnlock()
	return prometheusMonitor.artifactIndex[clipHash]
}

// isDVRHash checks if a string looks like a DVR hash (32-character hex)
func isDVRHash(hash string) bool {
	if len(hash) != 32 {
		return false
	}
	for _, char := range hash {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

// HandleDefaultStream handles the DEFAULT_STREAM trigger from MistServer
// This maps playback IDs to internal stream names for viewing (live streams)
// or clip hashes to VOD streams for clip viewing
func HandleDefaultStream(c *gin.Context) {
	start := time.Now()

	// Track node operations
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("default_stream", "requested").Inc()
	}

	// Read the raw body - MistServer sends parameters as raw text, not JSON
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to read DEFAULT_STREAM body")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("default_stream", "error").Inc()
		}

		c.String(http.StatusOK, "") // Empty response uses default behavior
		return
	}

	// Parse the parameters - they come as newline-separated values
	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 2 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    "at least 2",
		}).Error("Invalid DEFAULT_STREAM payload")
		c.String(http.StatusOK, "") // Empty response uses default behavior
		return
	}

	defaultStream := params[0]
	requestedStream := params[1]
	viewerHost := ""
	outputType := ""
	requestURL := ""

	if len(params) > 2 {
		viewerHost = params[2]
	}
	if len(params) > 3 {
		outputType = params[3]
	}
	if len(params) > 4 {
		requestURL = params[4]
	}

	logger.WithFields(logging.Fields{
		"default_stream":   defaultStream,
		"requested_stream": requestedStream,
		"viewer_host":      viewerHost,
		"output_type":      outputType,
		"request_url":      requestURL,
	}).Info("DEFAULT_STREAM trigger")

	// FIRST: Check if this is a clip hash we have locally (VOD content)
	if clipInfo := getClipFromArtifactIndex(defaultStream); clipInfo != nil {
		vodStreamName := fmt.Sprintf("vod+%s", defaultStream)

		logger.WithFields(logging.Fields{
			"clip_hash":       defaultStream,
			"vod_stream_name": vodStreamName,
			"clip_path":       clipInfo.FilePath,
			"stream_name":     clipInfo.StreamName,
		}).Info("Clip hash resolved to VOD stream")

		// Track VOD access
		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("default_stream", "vod_success").Inc()
			metrics.ResourceAllocationDuration.WithLabelValues("clip_resolution").Observe(time.Since(start).Seconds())
			metrics.InfrastructureEvents.WithLabelValues("clip_resolved").Inc()
		}

		// TODO: Forward clip view analytics to Decklog
		// This would track clip viewing events similar to stream views

		c.String(http.StatusOK, vodStreamName)
		return
	}

	// SECOND: Check if this is a DVR hash we have locally (also VOD content)
	// DVR hashes are 32-character hex strings, same as clip hashes but point to m3u8 manifests
	if len(defaultStream) == 32 && isDVRHash(defaultStream) {
		if dvrInfo := getClipFromArtifactIndex(defaultStream); dvrInfo != nil && dvrInfo.Format == "m3u8" {
			vodStreamName := fmt.Sprintf("vod+%s", defaultStream)

			logger.WithFields(logging.Fields{
				"dvr_hash":        defaultStream,
				"vod_stream_name": vodStreamName,
				"dvr_path":        dvrInfo.FilePath,
				"stream_name":     dvrInfo.StreamName,
			}).Info("DVR hash resolved to VOD stream")

			// Track DVR VOD access
			if metrics != nil {
				metrics.NodeOperations.WithLabelValues("default_stream", "dvr_vod_success").Inc()
				metrics.ResourceAllocationDuration.WithLabelValues("dvr_resolution").Observe(time.Since(start).Seconds())
				metrics.InfrastructureEvents.WithLabelValues("dvr_resolved").Inc()
			}

			// TODO: Forward DVR view analytics to Decklog
			// This would track DVR viewing events similar to stream views

			c.String(http.StatusOK, vodStreamName)
			return
		}
	}

	// THIRD: Fall back to existing live stream resolution via Commodore
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resolveResponse, err := commodoreClient.ResolvePlaybackID(ctx, defaultStream)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error":       err,
			"playback_id": defaultStream,
		}).Error("Failed to resolve playback ID")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("default_stream", "resolution_error").Inc()
		}

		c.String(http.StatusOK, "")
		return
	}

	// Get current node ID
	nodeID := getCurrentNodeID()

	// Forward analytics event to Decklog for stream view tracking (data plane only)
	// Create typed StreamViewPayload
	viewPayload := &validation.StreamViewPayload{
		TenantID:     resolveResponse.TenantID,
		PlaybackID:   defaultStream,
		InternalName: resolveResponse.InternalName,
		NodeID:       nodeID,
		ViewerHost:   viewerHost,
		OutputType:   outputType,
		RequestURL:   requestURL,
	}

	// Add geographic data from viewer IP if available
	if geoipReader != nil && viewerHost != "" {
		geoData := geoipReader.Lookup(viewerHost)
		if geoData != nil {
			viewPayload.CountryCode = geoData.CountryCode
			viewPayload.City = geoData.City
			viewPayload.Latitude = geoData.Latitude
			viewPayload.Longitude = geoData.Longitude

			logger.WithFields(logging.Fields{
				"viewer_ip":    viewerHost,
				"country_code": geoData.CountryCode,
				"city":         geoData.City,
				"playback_id":  defaultStream,
			}).Debug("Enriched DEFAULT_STREAM with viewer geo data")
		}
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:       uuid.New().String(),
		EventType:     validation.EventStreamView,
		Timestamp:     time.Now().UTC(),
		Source:        "mistserver_webhook",
		PlaybackID:    &defaultStream,
		InternalName:  &resolveResponse.InternalName,
		SchemaVersion: "2.0",
		StreamView:    viewPayload,
	}

	// Forward to Decklog with typed event
	go ForwardTypedEventToDecklog(baseEvent)

	// Return the wildcard stream name: live+{internal_name}
	wildcardStreamName := fmt.Sprintf("live+%s", resolveResponse.InternalName)
	logger.WithFields(logging.Fields{
		"playback_id":          defaultStream,
		"wildcard_stream_name": wildcardStreamName,
		"internal_name":        resolveResponse.InternalName,
	}).Info("Playback ID resolved successfully")

	// Track successful operation
	if metrics != nil {
		metrics.NodeOperations.WithLabelValues("default_stream", "success").Inc()
		metrics.ResourceAllocationDuration.WithLabelValues("stream_resolution").Observe(time.Since(start).Seconds())
		metrics.InfrastructureEvents.WithLabelValues("stream_resolved").Inc()
	}

	c.String(http.StatusOK, wildcardStreamName)
}

// HandleStreamSource handles the STREAM_SOURCE trigger from MistServer
// This resolves VOD stream names (vod+{artifact_hash}) to actual file paths for playback
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

	// Parse the parameters - they come as newline-separated values
	params := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(params) < 1 {
		logger.WithFields(logging.Fields{
			"param_count": len(params),
			"expected":    "at least 1",
		}).Error("Invalid STREAM_SOURCE payload")
		c.String(http.StatusOK, "") // Empty response will cause MistServer to use default source
		return
	}

	streamName := params[0]

	logger.WithFields(logging.Fields{
		"stream_name": streamName,
		"params":      params,
	}).Info("STREAM_SOURCE trigger")

	// Check if this is a VOD stream (vod+{artifact_hash})
	if strings.HasPrefix(streamName, "vod+") {
		artifactHash := strings.TrimPrefix(streamName, "vod+")

		// Look up artifact (clip or DVR) in artifact index
		if artifactInfo := getClipFromArtifactIndex(artifactHash); artifactInfo != nil {
			// Determine artifact type based on format
			artifactType := "clip"
			if artifactInfo.Format == "m3u8" {
				artifactType = "dvr"
			}

			logger.WithFields(logging.Fields{
				"artifact_hash":   artifactHash,
				"artifact_type":   artifactType,
				"stream_name":     streamName,
				"artifact_path":   artifactInfo.FilePath,
				"original_stream": artifactInfo.StreamName,
				"format":          artifactInfo.Format,
				"size_bytes":      artifactInfo.SizeBytes,
			}).Info("VOD artifact resolved to file path")

			// Track successful VOD resolution
			if metrics != nil {
				metrics.NodeOperations.WithLabelValues("stream_source", "vod_success").Inc()
				metrics.ResourceAllocationDuration.WithLabelValues("vod_resolution").Observe(time.Since(start).Seconds())
				if artifactType == "dvr" {
					metrics.InfrastructureEvents.WithLabelValues("dvr_source_resolved").Inc()
				} else {
					metrics.InfrastructureEvents.WithLabelValues("clip_source_resolved").Inc()
				}
			}

			// Return the file path for MistServer to read
			c.String(http.StatusOK, artifactInfo.FilePath)
			return
		}

		// Artifact not found in artifact index
		logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"stream_name":   streamName,
		}).Warn("VOD artifact not found in artifact index")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("stream_source", "vod_not_found").Inc()
		}
	} else {
		// Not a VOD stream - log for debugging
		logger.WithFields(logging.Fields{
			"stream_name": streamName,
		}).Debug("STREAM_SOURCE called for non-VOD stream")

		if metrics != nil {
			metrics.NodeOperations.WithLabelValues("stream_source", "non_vod").Inc()
		}
	}

	// Empty response - MistServer will use the default source configured for this stream
	c.String(http.StatusOK, "")
}

// enrichEventWithClusterMetadata adds cluster and node metadata to events
func enrichEventWithClusterMetadata(eventData map[string]interface{}) map[string]interface{} {
	nodeID := getCurrentNodeID()

	enriched := make(map[string]interface{})
	for k, v := range eventData {
		enriched[k] = v
	}

	enriched["cluster_id"] = clusterID
	enriched["foghorn_uri"] = foghornURI
	enriched["node_id"] = nodeID
	enriched["node_name"] = nodeName

	// Add geographic metadata from existing PrometheusMonitor
	if prometheusMonitor != nil {
		prometheusMonitor.mutex.RLock()
		if prometheusMonitor.latitude != nil {
			enriched["latitude"] = *prometheusMonitor.latitude
		}
		if prometheusMonitor.longitude != nil {
			enriched["longitude"] = *prometheusMonitor.longitude
		}
		if prometheusMonitor.location != "" {
			enriched["location"] = prometheusMonitor.location
		}
		prometheusMonitor.mutex.RUnlock()
	}

	return enriched
}

// updateFoghornStreamHealth immediately updates Foghorn with stream health status via gRPC
func updateFoghornStreamHealth(streamName string, isHealthy bool, details map[string]interface{}) error {
	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		internalName = streamName[plusIndex+1:]
	} else {
		internalName = streamName
	}

	// Send stream health update via gRPC control channel
	if err := control.SendStreamHealth(streamName, internalName, isHealthy, details); err != nil {
		logger.WithFields(logging.Fields{
			"stream_name":   streamName,
			"internal_name": internalName,
			"is_healthy":    isHealthy,
			"error":         err,
		}).Error("Failed to send stream health update via gRPC control channel")
		return fmt.Errorf("failed to send stream health update: %w", err)
	}

	logger.WithFields(logging.Fields{
		"stream_name":   streamName,
		"internal_name": internalName,
		"is_healthy":    isHealthy,
	}).Debug("Sent stream health update via gRPC control channel")

	return nil
}

// enrichEventWithGeoData adds geo information to event data using IP address
func enrichEventWithGeoData(eventData map[string]interface{}, ipAddress string) map[string]interface{} {
	enriched := make(map[string]interface{})
	for k, v := range eventData {
		enriched[k] = v
	}

	// Set geo fields to nil for consistent NULL handling initially
	enriched["country_code"] = nil
	enriched["city"] = nil
	enriched["latitude"] = nil
	enriched["longitude"] = nil

	geoSource := "none"

	// Try geoip lookup first if available
	if geoipReader != nil && ipAddress != "" {
		geoData := geoipReader.Lookup(ipAddress)
		if geoData != nil {
			// Add geo data to event, replacing nil values only if we have data
			if geoData.CountryCode != "" {
				enriched["country_code"] = geoData.CountryCode
			}

			if geoData.City != "" {
				enriched["city"] = geoData.City
			}

			if geoData.Latitude != 0 || geoData.Longitude != 0 {
				enriched["latitude"] = geoData.Latitude
				enriched["longitude"] = geoData.Longitude
			}

			geoSource = "geoip"
			logger.WithFields(logging.Fields{
				"ip_address":   ipAddress,
				"country_code": geoData.CountryCode,
				"city":         geoData.City,
				"latitude":     geoData.Latitude,
				"longitude":    geoData.Longitude,
				"source":       geoSource,
			}).Debug("Enriched event with geo data")

			return enriched
		}
	}

	// Fallback to MistServer node location if available
	if prometheusMonitor != nil && prometheusMonitor.latitude != nil && prometheusMonitor.longitude != nil {
		enriched["latitude"] = *prometheusMonitor.latitude
		enriched["longitude"] = *prometheusMonitor.longitude
		geoSource = "node_fallback"

		logger.WithFields(logging.Fields{
			"ip_address": ipAddress,
			"latitude":   *prometheusMonitor.latitude,
			"longitude":  *prometheusMonitor.longitude,
			"source":     geoSource,
		}).Debug("Used node location as geo fallback")
	}

	return enriched
}

// requestDVRStart requests Foghorn to start DVR recording for a stream via gRPC
func requestDVRStart(streamValidation *commodoreapi.ValidateStreamKeyResponse) {
	if err := control.SendDVRStartRequest(
		streamValidation.TenantID,
		streamValidation.InternalName,
		streamValidation.UserID,
		streamValidation.Recording.RetentionDays,
		streamValidation.Recording.Format,
		streamValidation.Recording.SegmentDuration,
	); err != nil {
		logger.WithFields(logging.Fields{
			"internal_name": streamValidation.InternalName,
			"tenant_id":     streamValidation.TenantID,
			"error":         err,
		}).Error("Failed to send DVR start request via gRPC control channel")
		return
	}

	logger.WithFields(logging.Fields{
		"internal_name":    streamValidation.InternalName,
		"tenant_id":        streamValidation.TenantID,
		"retention_days":   streamValidation.Recording.RetentionDays,
		"format":           streamValidation.Recording.Format,
		"segment_duration": streamValidation.Recording.SegmentDuration,
	}).Info("Successfully sent DVR start request via gRPC control channel")
}
