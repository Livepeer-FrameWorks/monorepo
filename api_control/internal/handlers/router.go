package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/models"
)

// Router handles tenant-to-cluster routing
type Router interface {
	GetBestClusterForStream(req models.StreamRequest) (*ClusterInfo, error)
	GetKafkaConfigForTenant(tenantID string) (brokers []string, topicPrefix string, err error)
}

// ClusterInfo represents a cluster that can serve a tenant
type ClusterInfo struct {
	ClusterID      string
	ClusterType    string
	BaseURL        string
	KafkaBrokers   []string
	TopicPrefix    string
	MaxStreams     int
	CurrentStreams int
	HealthStatus   string
}

// cacheEntry stores cached cluster info along with the last sync time
type cacheEntry struct {
	info     *ClusterInfo
	lastSync time.Time
}

// NewRouter creates a new router instance
func NewRouter(db *sql.DB, logger logging.Logger) (Router, error) {
	return &QuartermasterRouter{
		db:     db,
		logger: logger,
		cache:  make(map[string]cacheEntry),
	}, nil
}

// QuartermasterRouter implements routing using Quartermaster's API
type QuartermasterRouter struct {
	db     *sql.DB
	logger logging.Logger
	cache  map[string]cacheEntry
	mutex  sync.RWMutex
}

// GetBestClusterForStream returns the best cluster for a stream
func (r *QuartermasterRouter) GetBestClusterForStream(req models.StreamRequest) (*ClusterInfo, error) {
	// Call Quartermaster's routing API
	reqBody := map[string]interface{}{
		"tenant_id": req.TenantID,
		"stream_id": req.StreamID,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", quartermasterURL+"/api/v1/tenant/routing", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+serviceToken)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster routing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("routing request failed: %s", string(body))
	}

	var routing struct {
		ClusterID      string   `json:"cluster_id"`
		ClusterType    string   `json:"cluster_type"`
		BaseURL        string   `json:"base_url"`
		KafkaBrokers   []string `json:"kafka_brokers"`
		TopicPrefix    string   `json:"topic_prefix"`
		MaxStreams     int      `json:"max_streams"`
		CurrentStreams int      `json:"current_streams"`
		HealthStatus   string   `json:"health_status"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&routing); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	info := &ClusterInfo{
		ClusterID:      routing.ClusterID,
		ClusterType:    routing.ClusterType,
		BaseURL:        routing.BaseURL,
		KafkaBrokers:   routing.KafkaBrokers,
		TopicPrefix:    routing.TopicPrefix,
		MaxStreams:     routing.MaxStreams,
		CurrentStreams: routing.CurrentStreams,
		HealthStatus:   routing.HealthStatus,
	}

	// Cache the result
	r.mutex.Lock()
	r.cache[req.TenantID] = cacheEntry{info: info, lastSync: time.Now()}
	r.mutex.Unlock()

	return info, nil
}

// GetKafkaConfigForTenant returns Kafka routing for a tenant
func (r *QuartermasterRouter) GetKafkaConfigForTenant(tenantID string) (brokers []string, topicPrefix string, err error) {
	// Use cached cluster info if available
	r.mutex.RLock()
	if entry, exists := r.cache[tenantID]; exists && time.Since(entry.lastSync) < 5*time.Minute {
		r.mutex.RUnlock()
		return entry.info.KafkaBrokers, entry.info.TopicPrefix, nil
	}
	r.mutex.RUnlock()

	// Get fresh routing info
	info, err := r.GetBestClusterForStream(models.StreamRequest{TenantID: tenantID})
	if err != nil {
		return nil, "", err
	}

	return info.KafkaBrokers, info.TopicPrefix, nil
}
