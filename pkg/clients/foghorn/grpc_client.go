package foghorn

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	configpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	foghornrelaypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_relay"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

const InternalServerName = "foghorn.internal"

// GRPCClient is the gRPC client for Foghorn control plane services
type GRPCClient struct {
	conn      *grpc.ClientConn
	clip      foghornpb.ClipControlServiceClient
	dvr       foghornpb.DVRControlServiceClient
	viewer    foghornpb.ViewerControlServiceClient
	vod       foghornpb.VodControlServiceClient
	tenant    foghornpb.TenantControlServiceClient
	authority foghornpb.MediaAuthorityControlServiceClient
	placement foghornpb.MediaPlacementControlServiceClient
	edge      foghornpb.EdgeProvisioningServiceClient
	nodeMgmt  foghornpb.NodeControlServiceClient
	relay     foghornrelaypb.FoghornRelayClient
	logger    logging.Logger
	timeout   time.Duration
}

// GRPCConfig represents the configuration for the Foghorn gRPC client
type GRPCConfig struct {
	// GRPCAddr is the gRPC server address (host:port, no scheme)
	GRPCAddr string
	// Timeout for gRPC calls
	Timeout time.Duration
	// Logger for the client
	Logger logging.Logger
	// ServiceToken for service-to-service authentication.
	ServiceToken string
	// CircuitBreakerName identifies this destination in breaker metrics. Pooled
	// cross-cell clients set a stable cell-specific value.
	CircuitBreakerName string
	// UseTLS enables TLS transport. Uses system CA pool for validation.
	UseTLS     bool
	CACertFile string
	CACertPEM  string
	ServerName string
	// AllowInsecure disables TLS for operator-local/debug gRPC paths.
	AllowInsecure bool
}

// authInterceptor propagates service authentication to Foghorn. Commodore has
// already authorized and tenant-bound these calls, so caller identity metadata
// must not cross this service boundary beside the service bearer.
func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		md := metadata.MD{}
		if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
			md = existingMD.Copy()
		}
		md.Delete("authorization")
		md.Delete("x-user-id")
		md.Delete("x-tenant-id")

		if token := outgoingAuthToken(ctx, serviceToken); token != "" {
			md.Set("authorization", "Bearer "+token)
		}

		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func streamAuthInterceptor(serviceToken string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		md := metadata.MD{}
		if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
			md = existingMD.Copy()
		}
		md.Delete("authorization")
		md.Delete("x-user-id")
		md.Delete("x-tenant-id")

		if token := outgoingAuthToken(ctx, serviceToken); token != "" {
			md.Set("authorization", "Bearer "+token)
		}

		ctx = metadata.NewOutgoingContext(ctx, md)
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// timeoutInterceptor bounds calls made through Conn or Relay at the configured
// transport ceiling while preserving a caller's shorter operation deadline.
func timeoutInterceptor(timeout time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if timeout <= 0 {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

type operatorBearerKey struct{}

// WithOperatorBearer makes this client send jwt instead of its service
// credential for calls made with the returned context. Only operator tooling
// holding the operator's own session sets it. A service forwarding a caller's
// context never does, so a caller JWT still never crosses into Foghorn.
func WithOperatorBearer(ctx context.Context, jwt string) context.Context {
	return context.WithValue(ctx, operatorBearerKey{}, jwt)
}

func outgoingAuthToken(ctx context.Context, configuredServiceToken string) string {
	if bearer, ok := ctx.Value(operatorBearerKey{}).(string); ok && bearer != "" {
		return bearer
	}
	if configuredServiceToken != "" {
		return configuredServiceToken
	}
	if contextServiceToken := ctxkeys.GetServiceToken(ctx); contextServiceToken != "" {
		return contextServiceToken
	}
	return ""
}

// NewGRPCClient creates a new gRPC client for Foghorn
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	tlsCfg := foghornClientTLSConfig(config)
	transport, err := grpcutil.ClientTLS(tlsCfg, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("configure Foghorn gRPC TLS: %w", err)
	}
	breakerName := config.CircuitBreakerName
	if breakerName == "" {
		breakerName = "foghorn"
	}

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		transport,
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithChainUnaryInterceptor(
			timeoutInterceptor(config.Timeout),
			authInterceptor(config.ServiceToken),
			clients.FailsafeUnaryInterceptorWithMethodIsolation(
				breakerName,
				map[string]string{
					foghornpb.MediaAuthorityControlService_ApplyMediaAuthority_FullMethodName: breakerName + "-media-authority",
				},
				config.Logger,
			),
		),
		grpc.WithChainStreamInterceptor(
			streamAuthInterceptor(config.ServiceToken),
			clients.FailsafeStreamInterceptor(breakerName, config.Logger),
		),
		// PeerChannel is open for the life of a federation peering and carries no
		// frames at all on a cluster with no live streams. Without pings a path
		// black-holed by a NAT or firewall idle reap is never noticed: the receive
		// side blocks forever, the peer still reads as connected, and the first
		// lifecycle broadcast after that is enqueued into a mailbox whose Send is
		// already doomed. PermitWithoutStream is off; the stream is always open.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Foghorn gRPC: %w", err)
	}

	return &GRPCClient{
		conn:      conn,
		clip:      foghornpb.NewClipControlServiceClient(conn),
		dvr:       foghornpb.NewDVRControlServiceClient(conn),
		viewer:    foghornpb.NewViewerControlServiceClient(conn),
		vod:       foghornpb.NewVodControlServiceClient(conn),
		tenant:    foghornpb.NewTenantControlServiceClient(conn),
		authority: foghornpb.NewMediaAuthorityControlServiceClient(conn),
		placement: foghornpb.NewMediaPlacementControlServiceClient(conn),
		edge:      foghornpb.NewEdgeProvisioningServiceClient(conn),
		nodeMgmt:  foghornpb.NewNodeControlServiceClient(conn),
		relay:     foghornrelaypb.NewFoghornRelayClient(conn),
		logger:    config.Logger,
		timeout:   config.Timeout,
	}, nil
}

// ApplyMediaAuthority delivers one signed, cell-bound authority to Foghorn's
// durable local store. Duplicate versions are successful acknowledgements.
func (c *GRPCClient) ApplyMediaAuthority(ctx context.Context, authority *mediaauthoritypb.SignedAuthorityEnvelope) (*foghornpb.ApplyMediaAuthorityResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.authority.ApplyMediaAuthority(ctx, &foghornpb.ApplyMediaAuthorityRequest{Authority: authority})
}

func (c *GRPCClient) GetMediaCellPlacementCapability(ctx context.Context) (*foghornpb.MediaCellPlacementCapability, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.authority.GetMediaCellPlacementCapability(ctx, &emptypb.Empty{})
}

func foghornClientTLSConfig(config GRPCConfig) grpcutil.ClientTLSConfig {
	tlsCfg := grpcutil.ClientTLSConfig{
		CACertFile:        config.CACertFile,
		CACertPEM:         config.CACertPEM,
		ServerName:        config.ServerName,
		DefaultServerName: InternalServerName,
	}
	if config.AllowInsecure {
		tlsCfg.AllowInsecure = true
		return tlsCfg
	}
	if config.UseTLS || config.CACertFile != "" || config.CACertPEM != "" || config.ServerName != "" || grpcutil.AddrIsFQDN(config.GRPCAddr) || configpkg.IsProduction() {
		return tlsCfg
	}
	tlsCfg.AllowInsecure = true
	return tlsCfg
}

// Conn exposes the underlying connection for satellite clients
// (pkg/clients/foghorn/federation) to share.
func (c *GRPCClient) Conn() *grpc.ClientConn {
	return c.conn
}

// Relay returns the FoghornRelay client for intra-cluster HA command forwarding.
// Kept here (unlike Federation) because control.CommandRelayClient is built on it.
func (c *GRPCClient) Relay() foghornrelaypb.FoghornRelayClient {
	return c.relay
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// =============================================================================
// CLIP OPERATIONS
// =============================================================================

// CreateClip creates a new clip from a stream.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) CreateClip(ctx context.Context, req *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.clip.CreateClip(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// DeleteClip deletes a clip. requestedBy names the user the deletion is
// attributed to, empty for a system-initiated deletion.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) DeleteClip(ctx context.Context, clipHash string, tenantID *string, requestedBy string) (*sharedpb.DeleteClipResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &sharedpb.DeleteClipRequest{
		ClipHash:          clipHash,
		RequestedByUserId: requestedBy,
	}
	if tenantID != nil {
		req.TenantId = *tenantID
	}
	var trailers metadata.MD
	resp, err := c.clip.DeleteClip(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// DeleteStreamThumbnails removes a live stream's thumbnail control rows + objects on Foghorn (called at stream
// deletion, since a live stream has no artifact row for the purge job and its final version is never GC'd).
func (c *GRPCClient) DeleteStreamThumbnails(ctx context.Context, streamID, tenantID string) (*sharedpb.DeleteStreamThumbnailsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.clip.DeleteStreamThumbnails(ctx, &sharedpb.DeleteStreamThumbnailsRequest{StreamId: streamID, TenantId: tenantID})
}

// =============================================================================
// DVR OPERATIONS
// =============================================================================

// StartDVR initiates DVR recording for a stream.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) StartDVR(ctx context.Context, req *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.dvr.StartDVR(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// RetrieveDVRChapter returns the chapter row, including the canonical
// VOD playback_id once finalization has completed. UTC-only.
func (c *GRPCClient) RetrieveDVRChapter(ctx context.Context, req *foghorncontrolpb.RetrieveDVRChapterRequest) (*foghorncontrolpb.RetrieveDVRChapterResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var trailers metadata.MD
	resp, err := c.dvr.RetrieveDVRChapter(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// ListDVRChapters paginates chapter rows for player navigation. Bounded
// by page_size (default 200, max 1000) per the bounded-operations
// invariant for unbounded artifact lifetime.
func (c *GRPCClient) ListDVRChapters(ctx context.Context, req *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var trailers metadata.MD
	resp, err := c.dvr.ListDVRChapters(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// DiagnoseDVR reads one recording's lifecycle, segment ledger, and chapter
// finalization state. Foghorn admits only a platform-operator JWT, so callers
// pass a context built with WithOperatorBearer.
func (c *GRPCClient) DiagnoseDVR(ctx context.Context, dvrHash string) (*foghornpb.DiagnoseDVRResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.dvr.DiagnoseDVR(ctx, &foghornpb.DiagnoseDVRRequest{DvrHash: dvrHash})
}

// OverrideArtifactRetention pushes a per-asset retention horizon onto an
// existing foghorn.artifacts row so the next RetentionJob tick uses the new
// value. Called by Commodore.UpdateAssetRetention / ResetAssetRetention.
func (c *GRPCClient) OverrideArtifactRetention(ctx context.Context, req *foghornpb.OverrideArtifactRetentionRequest) (*foghornpb.OverrideArtifactRetentionResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var trailers metadata.MD
	resp, err := c.dvr.OverrideArtifactRetention(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// TestPlaybackAccess runs the dry-run policy evaluator against a caller-
// supplied JWT (or webhook test). Webhook mode (req.FireWebhook=true) makes
// a real outbound HTTPS call to the customer URL — Commodore validates
// tenant ownership upstream before forwarding here.
func (c *GRPCClient) TestPlaybackAccess(ctx context.Context, req *foghorncontrolpb.TestPlaybackAccessRequest) (*foghorncontrolpb.TestPlaybackAccessResponse, metadata.MD, error) {
	// Webhook mode can take up to ~10s if the customer endpoint is slow;
	// give the call its own headroom rather than tripping the default
	// client timeout.
	timeout := c.timeout
	if req.GetFireWebhook() && timeout < 15*time.Second {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var trailers metadata.MD
	resp, err := c.dvr.TestPlaybackAccess(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// StopDVR stops an active DVR recording.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) StopDVR(ctx context.Context, dvrHash string, tenantID *string, streamID *string) (*sharedpb.StopDVRResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &sharedpb.StopDVRRequest{
		DvrHash: dvrHash,
	}
	if tenantID != nil {
		req.TenantId = *tenantID
	}
	if streamID != nil && *streamID != "" {
		req.StreamId = streamID
	}
	var trailers metadata.MD
	resp, err := c.dvr.StopDVR(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// DeleteDVR deletes a DVR recording and its files. requestedBy names the
// user the deletion is attributed to, empty for a system-initiated deletion.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) DeleteDVR(ctx context.Context, dvrHash string, tenantID *string, requestedBy string) (*sharedpb.DeleteDVRResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &sharedpb.DeleteDVRRequest{
		DvrHash:           dvrHash,
		RequestedByUserId: requestedBy,
	}
	if tenantID != nil {
		req.TenantId = *tenantID
	}
	var trailers metadata.MD
	resp, err := c.dvr.DeleteDVR(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// =============================================================================
// VIEWER OPERATIONS
// =============================================================================

// ResolveViewerEndpoint resolves the best endpoint(s) for a viewer.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) ResolveViewerEndpoint(ctx context.Context, contentID string, viewerIP, viewerToken *string) (*sharedpb.ViewerEndpointResponse, metadata.MD, error) {
	return c.ResolveViewerEndpointWithProtocol(ctx, contentID, viewerIP, viewerToken, "")
}

func (c *GRPCClient) ResolveViewerEndpointWithProtocol(ctx context.Context, contentID string, viewerIP, viewerToken *string, protocol string) (*sharedpb.ViewerEndpointResponse, metadata.MD, error) {
	return c.ResolveViewer(ctx, &sharedpb.ViewerEndpointRequest{
		ContentId:   contentID,
		ViewerIp:    viewerIP,
		ViewerToken: viewerToken,
		Protocol:    protocol,
	})
}

// ResolveViewer sends a complete viewer request, including the viewer's
// Origin and Referer when the caller received them.
func (c *GRPCClient) ResolveViewer(ctx context.Context, req *sharedpb.ViewerEndpointRequest) (*sharedpb.ViewerEndpointResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.viewer.ResolveViewerEndpoint(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// ResolveIngestEndpoint resolves the best ingest endpoint(s) for StreamCrafter.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) ResolveIngestEndpoint(ctx context.Context, streamKey string, viewerIP *string, protocol sharedpb.IngestProtocol) (*sharedpb.IngestEndpointResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &sharedpb.IngestEndpointRequest{
		StreamKey: streamKey,
		ViewerIp:  viewerIP,
		Protocol:  protocol,
	}
	var trailers metadata.MD
	resp, err := c.viewer.ResolveIngestEndpoint(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// =============================================================================
// VOD OPERATIONS
// =============================================================================

// CreateVodUpload initiates a multipart upload and returns presigned URLs.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) CreateVodUpload(ctx context.Context, req *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.CreateVodUpload(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// ImportVodAsset records a VOD import from a URL and queues its processing.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) ImportVodAsset(ctx context.Context, req *sharedpb.ImportVodAssetRequest) (*sharedpb.ImportVodAssetResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.ImportVodAsset(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// CompleteVodUpload finalizes a multipart upload after all parts are uploaded.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) CompleteVodUpload(ctx context.Context, req *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.CompleteVodUpload(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// AbortVodUpload cancels an in-progress multipart upload. actor names the
// principal upload.aborted is attributed to, nil for none.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) AbortVodUpload(ctx context.Context, tenantID, uploadID string, actor *commonpb.RequestActor) (*sharedpb.AbortVodUploadResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.AbortVodUpload(ctx, &sharedpb.AbortVodUploadRequest{
		TenantId: tenantID,
		UploadId: uploadID,
		Actor:    actor,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// GetVodUploadStatus reads server-authoritative state of an in-flight multipart upload.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) GetVodUploadStatus(ctx context.Context, tenantID, uploadID string) (*sharedpb.GetVodUploadStatusResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.GetVodUploadStatus(ctx, &sharedpb.GetVodUploadStatusRequest{
		TenantId: tenantID,
		UploadId: uploadID,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// DeleteVodAsset deletes a VOD asset. requestedBy names the user the
// deletion is attributed to, empty for a system-initiated deletion.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) DeleteVodAsset(ctx context.Context, tenantID, artifactHash, requestedBy string) (*sharedpb.DeleteVodAssetResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.DeleteVodAsset(ctx, &sharedpb.DeleteVodAssetRequest{
		TenantId:          tenantID,
		ArtifactHash:      artifactHash,
		RequestedByUserId: requestedBy,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// InvalidatePlaybackAuth tells Foghorn to dispatch invalidate_sessions to
// Helmsman nodes that hold the listed live streams or artifacts. Empty
// internalNames lets Foghorn fan out across the tenant's known live streams
// and artifact sessions. The re-fired USER_NEW decides allow/deny per session.
func (c *GRPCClient) InvalidatePlaybackAuth(ctx context.Context, tenantID, reason string, internalNames []string) (*foghornpb.InvalidatePlaybackAuthResponse, metadata.MD, error) {
	return c.InvalidatePlaybackAuthWithBundle(ctx, tenantID, reason, internalNames, "", 0)
}

// InvalidatePlaybackAuthWithBundle retains the legacy stream/version fields
// on the wire. Foghorn's active behavior is session invalidation; signed media
// authority replacements and tombstones own policy convergence.
func (c *GRPCClient) InvalidatePlaybackAuthWithBundle(ctx context.Context, tenantID, reason string, internalNames []string, streamID string, bundleMinVersion int64) (*foghornpb.InvalidatePlaybackAuthResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.tenant.InvalidatePlaybackAuth(ctx, &foghornpb.InvalidatePlaybackAuthRequest{
		TenantId:         tenantID,
		InternalNames:    internalNames,
		Reason:           reason,
		StreamId:         streamID,
		BundleMinVersion: bundleMinVersion,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// TerminateTenantStreams stops all active streams for a suspended tenant.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) TerminateTenantStreams(ctx context.Context, tenantID, reason string) (*foghorncontrolpb.TerminateTenantStreamsResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.tenant.TerminateTenantStreams(ctx, &foghorncontrolpb.TerminateTenantStreamsRequest{
		TenantId: tenantID,
		Reason:   reason,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// InvalidateTenantCache clears cached suspension status for a tenant (called on reactivation).
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) InvalidateTenantCache(ctx context.Context, tenantID, reason string) (*foghorncontrolpb.InvalidateTenantCacheResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.tenant.InvalidateTenantCache(ctx, &foghorncontrolpb.InvalidateTenantCacheRequest{
		TenantId: tenantID,
		Reason:   reason,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// =============================================================================
// EDGE PROVISIONING
// =============================================================================

// PreRegisterEdge validates an enrollment token and returns an assigned domain for the edge.
func (c *GRPCClient) PreRegisterEdge(ctx context.Context, req *foghornpb.PreRegisterEdgeRequest) (*foghornpb.PreRegisterEdgeResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.edge.PreRegisterEdge(ctx, req)
}

// =============================================================================
// NODE MANAGEMENT
// =============================================================================

// SetNodeMode changes the operational mode of a node (normal, draining, maintenance).
func (c *GRPCClient) SetNodeMode(ctx context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var trailers metadata.MD
	resp, err := c.nodeMgmt.SetNodeOperationalMode(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// GetNodeHealth returns real-time health and routing state for a node.
func (c *GRPCClient) GetNodeHealth(ctx context.Context, req *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var trailers metadata.MD
	resp, err := c.nodeMgmt.GetNodeHealth(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}
