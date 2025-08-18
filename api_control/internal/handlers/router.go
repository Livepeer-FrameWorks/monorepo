package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	commodoreapi "frameworks/pkg/api/commodore"
	"frameworks/pkg/api/quartermaster"
	"frameworks/pkg/logging"
)

// Router handles tenant-to-cluster routing
type Router interface {
	GetBestClusterForStream(req commodoreapi.StreamRequest) (*ClusterInfo, error)
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

func (r *QuartermasterRouter) GetBestClusterForStream(req commodoreapi.StreamRequest) (*ClusterInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	routingReq := &quartermaster.TenantRoutingRequest{
		TenantID: req.TenantID,
		StreamID: req.StreamID,
	}

	routing, err := quartermasterClient.GetTenantRouting(ctx, routingReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster routing: %w", err)
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
	info, err := r.GetBestClusterForStream(commodoreapi.StreamRequest{TenantID: tenantID})
	if err != nil {
		return nil, "", err
	}

	return info.KafkaBrokers, info.TopicPrefix, nil
}
