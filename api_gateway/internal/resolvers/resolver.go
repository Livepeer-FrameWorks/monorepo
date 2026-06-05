package resolvers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/datafetcher"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetrytoken"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/metadata"
)

// GraphQLMetrics holds all Prometheus metrics for GraphQL operations.
// SignalmanClients tracks live bridge→Signalman gRPC fan-out clients
// (not browser WebSocket connections); SubscriptionsActive tracks live
// GraphQL subscription goroutines.
type GraphQLMetrics struct {
	Operations          *prometheus.CounterVec
	Duration            *prometheus.HistogramVec
	SignalmanClients    *prometheus.GaugeVec
	WebSocketMessages   *prometheus.CounterVec
	SubscriptionsActive *prometheus.GaugeVec
}

// Resolver represents the GraphQL resolver
type Resolver struct {
	Clients    *clients.ServiceClients
	Logger     logging.Logger
	SubManager *SubscriptionManager
	Metrics    *GraphQLMetrics
	Fetcher    *datafetcher.DataFetcher
	// TelemetrySecret signs player-telemetry attribution tokens. Empty disables
	// minting (cluster attribution then stays unproven downstream). Platform key,
	// never a customer playback-auth secret.
	TelemetrySecret []byte
	// LocalClusterID is this deployment's cluster, used as the serving cluster for
	// locally-resolved edges where the endpoint carries no explicit cluster_id.
	LocalClusterID string
}

// NewResolver creates a new GraphQL resolver
func NewResolver(serviceClients *clients.ServiceClients, logger logging.Logger, metrics *GraphQLMetrics, serviceToken string) *Resolver {
	// Initialize gRPC subscription manager. SIGNALMAN_GRPC_ADDRS /
	// SIGNALMAN_GRPC_ADDRS_BY_REGION carry the multi-replica lists used for
	// failover; the single-addr fields provide the required local target.
	signalmanAddr := config.RequireEnv("SIGNALMAN_GRPC_ADDR")
	signalmanAddrs := parseSignalmanAddrs(config.GetEnv("SIGNALMAN_GRPC_ADDRS", ""))
	signalmanByRegion := parseSignalmanAddrByRegion(config.GetEnv("SIGNALMAN_GRPC_ADDR_BY_REGION", ""))
	signalmanAddrsByRegion := parseSignalmanAddrsByRegion(config.GetEnv("SIGNALMAN_GRPC_ADDRS_BY_REGION", ""))
	maxConnections := config.GetEnvInt("WS_MAX_CONNECTIONS_PER_TENANT", 5)
	subManager := NewSubscriptionManager(logger, SubscriptionManagerConfig{
		SignalmanAddr:           signalmanAddr,
		SignalmanAddrsLocal:     signalmanAddrs,
		SignalmanAddrByRegion:   signalmanByRegion,
		SignalmanAddrsByRegion:  signalmanAddrsByRegion,
		ServiceToken:            serviceToken,
		MaxConnectionsPerTenant: maxConnections,
		Metrics:                 metrics,
	})
	// Wire stream-origin lookup so stream-scoped subscriptions attach to the
	// origin-region Signalman. Commodore's Stream proto carries
	// stream_origin_region (derived from active_ingest_cluster_id's
	// infrastructure_clusters.region_id). Resolver-level failures are
	// swallowed in connectionAddrForStream so the local Signalman remains
	// the always-available fallback.
	if serviceClients != nil && serviceClients.Commodore != nil {
		subManager.SetStreamOriginResolver(func(ctx context.Context, streamID string) (string, error) {
			stream, err := serviceClients.Commodore.GetStream(ctx, streamID)
			if err != nil {
				return "", err
			}
			if stream == nil {
				return "", nil
			}
			return stream.GetStreamOriginRegion(), nil
		})
	}

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
		Clients:         serviceClients,
		Logger:          logger,
		SubManager:      subManager,
		Metrics:         metrics,
		Fetcher:         fetcher,
		TelemetrySecret: []byte(config.GetEnv("TELEMETRY_TOKEN_SECRET", "")),
		LocalClusterID:  config.GetEnv("CLUSTER_ID", ""),
	}
}

// Shutdown gracefully shuts down the resolver and its resources
func (r *Resolver) Shutdown() error {
	if r.SubManager != nil {
		return r.SubManager.Shutdown()
	}
	return nil
}

// DoResolveViewerEndpoint calls Commodore to resolve viewer endpoints (which then calls Foghorn)
func (r *Resolver) DoResolveViewerEndpoint(ctx context.Context, contentID string, viewerIP *string) (*pb.ViewerEndpointResponse, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateViewerEndpointResponse(contentID), nil
	}

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

	// Resource-based x402 topup (viewer pays for stream owner balance)
	var httpReq *http.Request
	x402Paid := false
	if ginCtx, ok := ctx.Value(ctxkeys.KeyGinContext).(*gin.Context); ok && ginCtx != nil {
		httpReq = ginCtx.Request
		if v, ok := ginCtx.Get(string(ctxkeys.KeyX402Paid)); ok {
			if paid, ok := v.(bool); ok {
				x402Paid = paid
			}
		}
	}
	paymentHeader := ""
	if httpReq != nil {
		paymentHeader = middleware.GetX402PaymentHeader(httpReq)
	}

	if x402Paid {
		ctx = metadata.AppendToOutgoingContext(ctx, "x402-paid", "true")
		paymentHeader = ""
	}

	if paymentHeader != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-payment", paymentHeader)
	}
	// Call Commodore's viewer endpoint resolution (Commodore will handle tenant resolution internally)
	// gRPC client expects string (not *string) for viewerIP
	ip := ""
	if viewerIP != nil {
		ip = *viewerIP
	}
	viewerToken := playbackViewerTokenFromRequest(httpReq)
	resp, err := r.Clients.Commodore.ResolveViewerEndpoint(ctx, contentID, ip, viewerToken)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve viewer endpoints: %w", err)
	}
	r.stampTelemetryToken(contentID, resp)
	return resp, nil
}

// stampTelemetryToken mints a short-lived signed token binding this resolution's
// serving endpoint (node + cluster) and attaches it to the response metadata, so
// the player can echo it on its boot telemetry beacon and Bridge can trust
// cluster attribution. No-op when no signing secret is configured or no primary
// endpoint was resolved.
func (r *Resolver) stampTelemetryToken(contentID string, resp *pb.ViewerEndpointResponse) {
	if len(r.TelemetrySecret) == 0 || resp == nil || resp.GetMetadata() == nil {
		return
	}
	primary := resp.GetPrimary()
	if primary == nil {
		return
	}
	servingClusterID := primary.GetClusterId()
	if servingClusterID == "" {
		// Local edges carry no explicit cluster_id; the resolving deployment's
		// cluster is the serving cluster.
		servingClusterID = r.LocalClusterID
	}
	cid := resp.GetMetadata().GetContentId()
	if cid == "" {
		cid = contentID
	}
	token, err := telemetrytoken.Sign(r.TelemetrySecret, telemetrytoken.Claims{
		ContentID:        cid,
		NodeID:           primary.GetNodeId(),
		ServingClusterID: servingClusterID,
	}, 10*time.Minute, time.Now())
	if err != nil {
		r.Logger.WithError(err).Warn("failed to mint telemetry token")
		return
	}
	resp.Metadata.TelemetryToken = &token
}

func playbackViewerTokenFromRequest(req *http.Request) string {
	if req == nil {
		return ""
	}
	if token := strings.TrimSpace(req.Header.Get("X-Frameworks-Playback-JWT")); token != "" {
		return token
	}
	if token := strings.TrimSpace(req.Header.Get("X-Playback-JWT")); token != "" {
		return token
	}
	authz := strings.TrimSpace(req.Header.Get("X-Playback-Authorization"))
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return strings.TrimSpace(authz[len("Bearer "):])
	}
	return ""
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
		return nil, fmt.Errorf("failed to resolve ingest endpoints: %w", err)
	}
	return resp, nil
}

// strPtr returns a pointer to the given string (helper for model fields)
func strPtr(s string) *string {
	return &s
}
