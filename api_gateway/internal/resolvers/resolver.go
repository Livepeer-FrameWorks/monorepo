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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetrytoken"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/metadata"
)

// GraphQLMetrics holds all Prometheus metrics for GraphQL operations.
// SignalmanStreams tracks upstream bridge→Signalman subscription streams per
// tenant (one per tenant and channel with live subscribers, not browser
// WebSocket connections); SubscriptionsActive tracks served GraphQL
// subscriptions.
type GraphQLMetrics struct {
	Operations          *prometheus.CounterVec
	Duration            *prometheus.HistogramVec
	SignalmanStreams    *prometheus.GaugeVec
	WebSocketMessages   *prometheus.CounterVec
	SubscriptionsActive *prometheus.GaugeVec
	CacheLoadsActive    *prometheus.GaugeVec
	CacheLoadTimeouts   *prometheus.CounterVec
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
	// Streaming supplies the streamingConfig ports and root domain on each
	// request, so an env-file reload takes effect without a restart.
	Streaming func() StreamingSettings
	// capabilityCache holds the capabilities query's per-tenant sections. It
	// describes enforcement to clients; no enforcement path reads it.
	capabilityCache capabilitySections
}

// StreamingSettings are the values the streamingConfig query reads on each
// request.
type StreamingSettings struct {
	SRTPort  int
	RTMPPort int
	// RootDomain builds global streaming host names when cluster routing
	// carries no base URL.
	RootDomain string
}

// ResolverConfig is the configuration NewResolver needs.
type ResolverConfig struct {
	ServiceToken string
	// SignalmanAddr is the required local-region Signalman service alias. The
	// raw comma-separated SignalmanAddrs value optionally lists replica targets
	// used instead of it.
	SignalmanAddr  string
	SignalmanAddrs string
	// MaxSubscriptionsPerTenant caps concurrent GraphQL subscriptions per tenant
	// on this Bridge replica; zero disables the cap.
	MaxSubscriptionsPerTenant int
	// SignalmanDial configures the pooled Signalman connections at startup.
	SignalmanDial SignalmanDialSettings

	PeriscopeCache       cache.Options
	PeriscopeLoadTimeout time.Duration

	TelemetrySecret []byte
	LocalClusterID  string
	// Streaming is read on each streamingConfig request.
	Streaming func() StreamingSettings
}

// NewResolver creates a new GraphQL resolver
func NewResolver(serviceClients *clients.ServiceClients, logger logging.Logger, metrics *GraphQLMetrics, cfg ResolverConfig) *Resolver {
	// Bridge serves every GraphQL subscription from its own region's Signalman.
	// SignalmanAddrs optionally lists replica targets; otherwise the required
	// SignalmanAddr service alias spreads streams across the local replicas.
	signalmanAddrs := parseSignalmanAddrs(cfg.SignalmanAddrs)
	if len(signalmanAddrs) == 0 {
		signalmanAddrs = []string{cfg.SignalmanAddr}
	}
	subManager := NewSubscriptionManager(logger, SubscriptionManagerConfig{
		SignalmanAddrs:            signalmanAddrs,
		ServiceToken:              cfg.ServiceToken,
		MaxSubscriptionsPerTenant: cfg.MaxSubscriptionsPerTenant,
		Metrics:                   metrics,
		Dial:                      cfg.SignalmanDial,
	})

	periscopeCache := cache.New(cfg.PeriscopeCache, cache.MetricsHooks{})

	fetcher := datafetcher.New(datafetcher.Config{
		Logger:      logger,
		LoadTimeout: cfg.PeriscopeLoadTimeout,
		Caches: map[datafetcher.Service]*cache.Cache{
			datafetcher.ServicePeriscope: periscopeCache,
		},
		OnLoadStarted: func(service datafetcher.Service, operation string) {
			if metrics != nil && metrics.CacheLoadsActive != nil {
				metrics.CacheLoadsActive.WithLabelValues(string(service), operation).Inc()
			}
		},
		OnLoadFinished: func(service datafetcher.Service, operation string, timedOut bool) {
			if metrics != nil && metrics.CacheLoadsActive != nil {
				metrics.CacheLoadsActive.WithLabelValues(string(service), operation).Dec()
			}
			if timedOut && metrics != nil && metrics.CacheLoadTimeouts != nil {
				metrics.CacheLoadTimeouts.WithLabelValues(string(service), operation).Inc()
			}
		},
	})

	return &Resolver{
		Clients:         serviceClients,
		Logger:          logger,
		SubManager:      subManager,
		Metrics:         metrics,
		Fetcher:         fetcher,
		TelemetrySecret: cfg.TelemetrySecret,
		LocalClusterID:  cfg.LocalClusterID,
		Streaming:       cfg.Streaming,
	}
}

// streamingSettings returns the current streamingConfig values. A resolver
// built without a Streaming getter reports zero ports and no root domain.
func (r *Resolver) streamingSettings() StreamingSettings {
	if r == nil || r.Streaming == nil {
		return StreamingSettings{}
	}
	return r.Streaming()
}

// Shutdown gracefully shuts down the resolver and its resources
func (r *Resolver) Shutdown() error {
	if r.SubManager != nil {
		return r.SubManager.Shutdown()
	}
	return nil
}

// DoResolveViewerEndpoint calls Commodore to resolve viewer endpoints (which then calls Foghorn)
func (r *Resolver) DoResolveViewerEndpoint(ctx context.Context, contentID string, viewerIP *string) (*sharedpb.ViewerEndpointResponse, error) {
	return r.DoResolveViewerEndpointForProtocol(ctx, contentID, viewerIP, "")
}

func (r *Resolver) DoResolveViewerEndpointForProtocol(ctx context.Context, contentID string, viewerIP *string, requestedProtocol string) (*sharedpb.ViewerEndpointResponse, error) {
	protocol, err := viewerProtocolRequirement(requestedProtocol)
	if err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return viewerDemoForProtocol(demo.GenerateViewerEndpointResponse(contentID), protocol)
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

	// Resource-based x402 topup (viewer pays for stream owner balance).
	// Gateway consumes a submitted payment and Foghorn independently enforces
	// the owner's canonical billing state; no paid boolean crosses services.
	var httpReq *http.Request
	if ginCtx, ok := ctx.Value(ctxkeys.KeyGinContext).(*gin.Context); ok && ginCtx != nil {
		httpReq = ginCtx.Request
	}
	paymentHeader := ""
	if httpReq != nil {
		paymentHeader = middleware.GetX402PaymentHeader(httpReq)
	}

	if paymentHeader != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-payment", paymentHeader)
	}
	// Call Commodore's viewer endpoint resolution (Commodore will handle tenant resolution internally)
	req := &sharedpb.ViewerEndpointRequest{ContentId: contentID, Protocol: protocol}
	if viewerIP != nil && *viewerIP != "" {
		req.ViewerIp = viewerIP
	}
	if viewerToken := playbackViewerTokenFromRequest(httpReq); viewerToken != "" {
		req.ViewerToken = &viewerToken
	}
	// The browser's Origin and Referer let Foghorn apply the policy's allowed
	// origins; a server-side call without them is not origin-checked at resolve.
	if httpReq != nil {
		origin, referer := httpReq.Header.Get("Origin"), httpReq.Header.Get("Referer")
		req.ViewerOrigin, req.ViewerReferer = &origin, &referer
	}
	resp, err := r.Clients.Commodore.ResolveViewer(ctx, req)
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
func (r *Resolver) stampTelemetryToken(contentID string, resp *sharedpb.ViewerEndpointResponse) {
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

func (r *Resolver) DoResolveIngestEndpoint(ctx context.Context, streamKey string, viewerIP *string) (*sharedpb.IngestEndpointResponse, error) {
	return r.DoResolveIngestEndpointForProtocol(ctx, streamKey, viewerIP, "")
}

func (r *Resolver) DoResolveIngestEndpointForProtocol(ctx context.Context, streamKey string, viewerIP *string, protocol string) (*sharedpb.IngestEndpointResponse, error) {
	var requested sharedpb.IngestProtocol
	switch protocol {
	case "":
		requested = sharedpb.IngestProtocol_INGEST_PROTOCOL_UNSPECIFIED
	case "WHIP":
		requested = sharedpb.IngestProtocol_INGEST_PROTOCOL_WHIP
	case "RTMP":
		requested = sharedpb.IngestProtocol_INGEST_PROTOCOL_RTMP
	case "SRT":
		requested = sharedpb.IngestProtocol_INGEST_PROTOCOL_SRT
	default:
		return nil, fmt.Errorf("unsupported ingest protocol")
	}
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
	resp, err := r.Clients.Commodore.ResolveIngestEndpoint(ctx, streamKey, ip, requested)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ingest endpoints: %w", err)
	}
	return resp, nil
}

// strPtr returns a pointer to the given string (helper for model fields)
func strPtr(s string) *string {
	return &s
}
