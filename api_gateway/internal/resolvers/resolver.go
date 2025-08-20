package resolvers

import (
	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
)

// GraphQLMetrics holds all Prometheus metrics for GraphQL operations
type GraphQLMetrics struct {
	Operations           *prometheus.CounterVec
	Duration             *prometheus.HistogramVec
	WebSocketConnections *prometheus.GaugeVec
	WebSocketMessages    *prometheus.CounterVec
	SubscriptionsActive  *prometheus.GaugeVec
}

// Resolver represents the GraphQL resolver
type Resolver struct {
	Clients   *clients.ServiceClients
	Logger    logging.Logger
	wsManager *WebSocketManager
	Metrics   *GraphQLMetrics
}

// NewResolver creates a new GraphQL resolver
func NewResolver(serviceClients *clients.ServiceClients, logger logging.Logger, metrics *GraphQLMetrics) *Resolver {
	// Initialize WebSocket manager
	signalmanURL := config.GetEnv("SIGNALMAN_WS_URL", "ws://localhost:18009")
	wsManager := NewWebSocketManager(signalmanURL, logger, metrics)

	return &Resolver{
		Clients:   serviceClients,
		Logger:    logger,
		wsManager: wsManager,
		Metrics:   metrics,
	}
}

// Shutdown gracefully shuts down the resolver and its resources
func (r *Resolver) Shutdown() error {
	if r.wsManager != nil {
		return r.wsManager.Shutdown()
	}
	return nil
}
