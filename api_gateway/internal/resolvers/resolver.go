package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/datafetcher"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/cache"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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
	Clients    *clients.ServiceClients
	Logger     logging.Logger
	SubManager *SubscriptionManager
	Metrics    *GraphQLMetrics
	Fetcher    *datafetcher.DataFetcher
}

// NewResolver creates a new GraphQL resolver
func NewResolver(serviceClients *clients.ServiceClients, logger logging.Logger, metrics *GraphQLMetrics, serviceToken string) *Resolver {
	// Initialize gRPC subscription manager
	signalmanAddr := config.RequireEnv("SIGNALMAN_GRPC_ADDR")
	maxConnections := config.GetEnvInt("WS_MAX_CONNECTIONS_PER_TENANT", 5)
	subManager := NewSubscriptionManager(signalmanAddr, serviceToken, logger, metrics, maxConnections)

	periscopeTTL := time.Duration(config.GetEnvInt("PERISCOPE_CACHE_TTL_SECONDS", 30)) * time.Second
	periscopeSWR := time.Duration(config.GetEnvInt("PERISCOPE_CACHE_SWR_SECONDS", 15)) * time.Second
	periscopeNeg := time.Duration(config.GetEnvInt("PERISCOPE_CACHE_NEG_TTL_SECONDS", 5)) * time.Second
	periscopeMax := config.GetEnvInt("PERISCOPE_CACHE_MAX", 5000)
	periscopeCache := cache.New(cache.Options{TTL: periscopeTTL, StaleWhileRevalidate: periscopeSWR, NegativeTTL: periscopeNeg, MaxEntries: periscopeMax}, cache.MetricsHooks{})

	fetcher := datafetcher.New(datafetcher.Config{
		Logger: logger,
		Caches: map[datafetcher.Service]*cache.Cache{
			datafetcher.ServicePeriscope: periscopeCache,
		},
	})

	return &Resolver{
		Clients:    serviceClients,
		Logger:     logger,
		SubManager: subManager,
		Metrics:    metrics,
		Fetcher:    fetcher,
	}
}

// Shutdown gracefully shuts down the resolver and its resources
func (r *Resolver) Shutdown() error {
	if r.SubManager != nil {
		return r.SubManager.Shutdown()
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
func (r *Resolver) DoResolveViewerEndpoint(ctx context.Context, contentType, contentID string, viewerIP *string) (*pb.ViewerEndpointResponse, error) {
	// Diagnostic checks for panic root cause
	if r == nil {
		return nil, fmt.Errorf("CRITICAL: Resolver (r) is nil")
	}
	if r.Clients == nil {
		return nil, fmt.Errorf("CRITICAL: Resolver.Clients is nil")
	}
	if r.Clients.Commodore == nil {
		return nil, fmt.Errorf("CRITICAL: Resolver.Clients.Commodore is nil - ServiceClients initialization failed silently?")
	}

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateViewerEndpointResponse(contentType, contentID), nil
	}
	// Call Commodore's viewer endpoint resolution (Commodore will handle tenant resolution internally)
	// gRPC client expects string (not *string) for viewerIP
	ip := ""
	if viewerIP != nil {
		ip = *viewerIP
	}
	resp, err := r.Clients.Commodore.ResolveViewerEndpoint(ctx, contentType, contentID, ip)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve viewer endpoints: %v", err)
	}
	return resp, nil
}

func (r *Resolver) DoResolveIngestEndpoint(ctx context.Context, streamKey string, viewerIP *string) (*pb.IngestEndpointResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("CRITICAL: Resolver (r) is nil")
	}
	if r.Clients == nil {
		return nil, fmt.Errorf("CRITICAL: Resolver.Clients is nil")
	}
	if r.Clients.Commodore == nil {
		return nil, fmt.Errorf("CRITICAL: Resolver.Clients.Commodore is nil")
	}

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateIngestEndpointResponse(streamKey), nil
	}

	ip := ""
	if viewerIP != nil {
		ip = *viewerIP
	}
	resp, err := r.Clients.Commodore.ResolveIngestEndpoint(ctx, streamKey, ip)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ingest endpoints: %v", err)
	}
	return resp, nil
}

// strPtr returns a pointer to the given string (helper for model fields)
func strPtr(s string) *string {
	return &s
}
