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

	commodoreapi "frameworks/pkg/api/commodore"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/foghorn"
	"frameworks/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/gin-gonic/gin"
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

	// Initialize the Decklog client for analytics forwarding
	InitDecklogClient()

	// Initialize Prometheus monitoring
	InitPrometheusMonitor(logger)

	logger.WithFields(logging.Fields{
		"commodore_url": apiBaseURL,
		"cluster_id":    clusterID,
		"foghorn_uri":   foghornURI,
		"node_name":     nodeName,
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
		return "unknown"
	}

	nodes := prometheusMonitor.GetNodes()
	// Each sidecar monitors exactly 1 node - return the first (and only) one
	for nodeID := range nodes {
		return nodeID
	}
	return "unknown"
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
	validation, err := validateStreamKeyViaAPI(streamKey)
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

	if !validation.Valid {
		logger.WithFields(logging.Fields{
			"stream_key": streamKey,
			"api_error":  validation.Error,
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
		"internal_name": validation.InternalName,
		"hostname":      hostname,
		"push_url":      pushURL,
		"event_type":    "push_rewrite_success",
		"timestamp":     time.Now().Unix(),
	})

	// Forward analytics event to Decklog for batched processing
	go ForwardEventToDecklog("stream-ingest", map[string]interface{}{
		"stream_key":    streamKey,
		"internal_name": validation.InternalName,
		"hostname":      hostname,
		"push_url":      pushURL,
		"protocol":      protocol,
		"event_type":    "stream-ingest",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_webhook",
		"node_id":       nodeID,
	})

	// Create wildcard stream name for MistServer routing
	wildcardStreamName := fmt.Sprintf("live+%s", validation.InternalName)

	logger.WithFields(logging.Fields{
		"stream_key":           streamKey,
		"wildcard_stream_name": wildcardStreamName,
		"user_id":              validation.UserID,
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

// HandleDefaultStream handles the DEFAULT_STREAM trigger from MistServer
// This maps playback IDs to internal stream names for viewing
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
	go ForwardEventToDecklog("stream-view", map[string]interface{}{
		"tenant_id":     resolveResponse.TenantID,
		"playback_id":   defaultStream,
		"internal_name": resolveResponse.InternalName,
		"node_id":       nodeID,
		"viewer_host":   viewerHost,
		"output_type":   outputType,
		"event_type":    "stream-view-request",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_webhook",
	})

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

// updateFoghornStreamHealth immediately updates Foghorn with stream health status
func updateFoghornStreamHealth(streamName string, isHealthy bool, details map[string]interface{}) error {
	nodeID := getCurrentNodeID()

	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		internalName = streamName[plusIndex+1:]
	} else {
		internalName = streamName
	}

	// Prepare update payload
	update := map[string]interface{}{
		"node_id":       nodeID,
		"stream_name":   streamName,
		"internal_name": internalName,
		"is_healthy":    isHealthy,
		"timestamp":     time.Now().Unix(),
	}

	// Add any additional details
	for k, v := range details {
		update[k] = v
	}

	// Send to Foghorn using shared client
	req := &foghorn.StreamHealthRequest{
		NodeID:       nodeID,
		StreamName:   streamName,
		InternalName: internalName,
		IsHealthy:    isHealthy,
		Timestamp:    time.Now().Unix(),
		Details:      details,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := foghornClient.UpdateStreamHealth(ctx, req); err != nil {
		return fmt.Errorf("failed to send stream health update to Foghorn: %w", err)
	}

	logger.WithFields(logging.Fields{
		"stream_name": streamName,
		"is_healthy":  isHealthy,
		"node_id":     nodeID,
	}).Info("Updated Foghorn with stream health status")

	return nil
}
