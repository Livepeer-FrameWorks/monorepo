package resolvers

import (
	"context"
	"fmt"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	commodore "frameworks/pkg/api/commodore"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	"time"

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
	WSManager *WebSocketManager
	Metrics   *GraphQLMetrics
}

// NewResolver creates a new GraphQL resolver
func NewResolver(serviceClients *clients.ServiceClients, logger logging.Logger, metrics *GraphQLMetrics) *Resolver {
	// Initialize WebSocket manager
	signalmanURL := config.RequireEnv("SIGNALMAN_WS_URL")
	wsManager := NewWebSocketManager(signalmanURL, logger, metrics)

	return &Resolver{
		Clients:   serviceClients,
		Logger:    logger,
		WSManager: wsManager,
		Metrics:   metrics,
	}
}

// Shutdown gracefully shuts down the resolver and its resources
func (r *Resolver) Shutdown() error {
	if r.WSManager != nil {
		return r.WSManager.Shutdown()
	}
	return nil
}

type TimeRangeParams struct {
	Start *time.Time
	End   *time.Time
	// Max duration allowed
	MaxWindow time.Duration
	// Default window when none provided
	DefaultWindow time.Duration
}

func (r *Resolver) normalizeTimeRange(p TimeRangeParams) (start *time.Time, end *time.Time, err error) {
	now := time.Now()
	// Apply defaults
	if p.Start == nil && p.End == nil {
		if p.DefaultWindow == 0 {
			p.DefaultWindow = 24 * time.Hour
		}
		to := now
		from := now.Add(-p.DefaultWindow)
		return &from, &to, nil
	}
	if p.End == nil {
		to := now
		end = &to
	} else {
		end = p.End
	}
	if p.Start == nil {
		win := p.DefaultWindow
		if win == 0 {
			win = 24 * time.Hour
		}
		from := end.Add(-win)
		start = &from
	} else {
		start = p.Start
	}
	// Validate order
	if start.After(*end) {
		return nil, nil, fmt.Errorf("invalid time range: start after end")
	}
	// Enforce max window
	max := p.MaxWindow
	if max == 0 {
		max = 31 * 24 * time.Hour
	}
	if end.Sub(*start) > max {
		clamped := end.Add(-max)
		start = &clamped
	}
	return start, end, nil
}

// DoResolveViewerEndpoint calls Commodore to resolve viewer endpoints (which then calls Foghorn)
func (r *Resolver) DoResolveViewerEndpoint(ctx context.Context, contentType, contentID string, viewerIP *string) (*commodore.ViewerEndpointResponse, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateViewerEndpointResponse(contentType, contentID), nil
	}
	// Call Commodore's viewer endpoint resolution (Commodore will handle tenant resolution internally)
	resp, err := r.Clients.Commodore.ResolveViewerEndpoint(ctx, contentType, contentID, viewerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve viewer endpoints: %v", err)
	}
	return resp, nil
}
