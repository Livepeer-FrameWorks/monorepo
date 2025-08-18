package resolvers

import (
	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
)

// Resolver represents the GraphQL resolver
type Resolver struct {
	Clients   *clients.ServiceClients
	Logger    logging.Logger
	wsManager *WebSocketManager
}

// NewResolver creates a new GraphQL resolver
func NewResolver(serviceClients *clients.ServiceClients, logger logging.Logger) *Resolver {
	// Initialize WebSocket manager
	signalmanURL := config.GetEnv("SIGNALMAN_WS_URL", "ws://localhost:18009")
	wsManager := NewWebSocketManager(signalmanURL, logger)

	return &Resolver{
		Clients:   serviceClients,
		Logger:    logger,
		wsManager: wsManager,
	}
}

// Shutdown gracefully shuts down the resolver and its resources
func (r *Resolver) Shutdown() error {
	if r.wsManager != nil {
		return r.wsManager.Shutdown()
	}
	return nil
}
