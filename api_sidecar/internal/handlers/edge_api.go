package handlers

import (
	"net/http"
	"strings"
	"time"

	"frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"frameworks/pkg/version"

	"github.com/gin-gonic/gin"
)

const (
	edgeAPIContextUserID   = "edge_user_id"
	edgeAPIContextTenantID = "edge_tenant_id"
)

var edgeAPIStartTime = time.Now()

// HandleEdgeStatus returns node operational state.
func HandleEdgeStatus(c *gin.Context) {
	mode := config.GetOperationalMode()
	c.JSON(http.StatusOK, gin.H{
		"node_id":          control.GetNodeID(),
		"operational_mode": modeToString(mode),
		"tenant_id":        config.GetTenantID(),
		"uptime_seconds":   int(time.Since(edgeAPIStartTime).Seconds()),
		"version":          version.Version,
		"git_commit":       version.GitCommit,
	})
}

// HandleEdgeHealth returns health status of the local services.
func HandleEdgeHealth(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "monitor not initialized"})
		return
	}

	prometheusMonitor.mutex.RLock()
	healthy := prometheusMonitor.isHealthy
	lastSeen := prometheusMonitor.lastSeen
	prometheusMonitor.mutex.RUnlock()

	c.JSON(http.StatusOK, gin.H{
		"healthy":        healthy,
		"last_seen":      lastSeen,
		"node_id":        control.GetNodeID(),
		"mist_reachable": healthy,
	})
}

// HandleEdgeStreams returns active streams with viewer counts and bandwidth.
func HandleEdgeStreams(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "monitor not initialized"})
		return
	}

	prometheusMonitor.mistMu.Lock()
	apiResponse, err := prometheusMonitor.mistClient.GetActiveStreams()
	prometheusMonitor.mistMu.Unlock()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query MistServer", "detail": err.Error()})
		return
	}

	activeStreams, _ := apiResponse["active_streams"].(map[string]interface{})
	streams := make([]gin.H, 0, len(activeStreams))
	for streamName, data := range activeStreams {
		info, ok := data.(map[string]interface{})
		if !ok {
			continue
		}
		entry := gin.H{
			"name": streamName,
		}
		if v, ok := info["viewers"].(float64); ok {
			entry["viewers"] = int(v)
		}
		if v, ok := info["clients"].(float64); ok {
			entry["clients"] = int(v)
		}
		if v, ok := info["upbytes"].(float64); ok {
			entry["up_bytes"] = uint64(v)
		}
		if v, ok := info["downbytes"].(float64); ok {
			entry["down_bytes"] = uint64(v)
		}
		if v, ok := info["inputs"].(float64); ok {
			entry["inputs"] = int(v)
		}
		if v, ok := info["outputs"].(float64); ok {
			entry["outputs"] = int(v)
		}
		streams = append(streams, entry)
	}

	c.JSON(http.StatusOK, gin.H{
		"node_id": control.GetNodeID(),
		"count":   len(streams),
		"streams": streams,
	})
}

// HandleEdgeStreamDetail returns detailed info for a single stream.
func HandleEdgeStreamDetail(c *gin.Context) {
	streamName := c.Param("stream_name")
	if streamName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stream_name required"})
		return
	}

	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "monitor not initialized"})
		return
	}

	prometheusMonitor.mistMu.Lock()
	info, err := prometheusMonitor.mistClient.GetStreamInfo(streamName)
	prometheusMonitor.mistMu.Unlock()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query stream", "detail": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

// HandleEdgeClients returns active client connections.
func HandleEdgeClients(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "monitor not initialized"})
		return
	}

	prometheusMonitor.mistMu.Lock()
	clientData, err := prometheusMonitor.mistClient.GetClients()
	prometheusMonitor.mistMu.Unlock()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query clients", "detail": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"node_id": control.GetNodeID(),
		"clients": clientData,
	})
}

// HandleEdgeMetrics returns a bandwidth and resource snapshot from the last poll.
func HandleEdgeMetrics(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "monitor not initialized"})
		return
	}

	prometheusMonitor.mutex.RLock()
	jsonData := prometheusMonitor.lastJSONData
	bwUp := prometheusMonitor.lastBwUp
	bwDown := prometheusMonitor.lastBwDown
	lastPoll := prometheusMonitor.lastPollTime
	prometheusMonitor.mutex.RUnlock()

	result := gin.H{
		"node_id":        control.GetNodeID(),
		"last_poll":      lastPoll,
		"bandwidth_up":   bwUp,
		"bandwidth_down": bwDown,
	}

	if jsonData != nil {
		if totals, ok := jsonData["totals"].(map[string]interface{}); ok {
			if cpu, ok := totals["cpu"].(float64); ok {
				result["cpu_percent"] = cpu
			}
			if mem, ok := totals["mem"].(float64); ok {
				result["mem_bytes"] = uint64(mem)
			}
			if viewers, ok := totals["viewers"].(float64); ok {
				result["total_viewers"] = int(viewers)
			}
		}
	}

	c.JSON(http.StatusOK, result)
}

// EdgeAPIAuthMiddleware validates a bearer token (JWT or API token) for the Edge API.
// Helmsman forwards the token to Foghorn for validation; results are cached with TTL.
func EdgeAPIAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authorization required"})
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		if token == auth {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bearer token required"})
			return
		}

		resp, err := control.ValidateEdgeToken(c.Request.Context(), token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "token validation unavailable"})
			return
		}
		if !resp.Valid {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid token"})
			return
		}

		c.Set(edgeAPIContextUserID, resp.UserId)
		c.Set(edgeAPIContextTenantID, resp.TenantId)
		c.Next()
	}
}
