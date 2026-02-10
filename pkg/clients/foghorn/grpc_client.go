package foghorn

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/clients"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// GRPCClient is the gRPC client for Foghorn control plane services
type GRPCClient struct {
	conn    *grpc.ClientConn
	clip    pb.ClipControlServiceClient
	dvr     pb.DVRControlServiceClient
	viewer  pb.ViewerControlServiceClient
	vod     pb.VodControlServiceClient
	tenant  pb.TenantControlServiceClient
	logger  logging.Logger
	timeout time.Duration
}

// GRPCConfig represents the configuration for the Foghorn gRPC client
type GRPCConfig struct {
	// GRPCAddr is the gRPC server address (host:port, no scheme)
	GRPCAddr string
	// Timeout for gRPC calls
	Timeout time.Duration
	// Logger for the client
	Logger logging.Logger
	// ServiceToken for service-to-service authentication (fallback when no user JWT)
	ServiceToken string
}

// authInterceptor propagates authentication to gRPC metadata.
// This reads user_id, tenant_id, and jwt_token from the Go context (set by Gateway middleware)
// and adds them to outgoing gRPC metadata for downstream services.
// If no user JWT is available, it falls back to the service token for service-to-service calls.
func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Extract user context from Go context and add to gRPC metadata
		md := metadata.MD{}

		if userID := ctxkeys.GetUserID(ctx); userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}

		// Use user's JWT from context if available, otherwise fall back to service token
		if jwtToken := ctxkeys.GetJWTToken(ctx); jwtToken != "" {
			md.Set("authorization", "Bearer "+jwtToken)
		} else if serviceToken != "" {
			md.Set("authorization", "Bearer "+serviceToken)
		}

		// Merge with existing outgoing metadata if any
		if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
			md = metadata.Join(existingMD, md)
		}

		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// NewGRPCClient creates a new gRPC client for Foghorn
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithChainUnaryInterceptor(
			authInterceptor(config.ServiceToken),
			clients.FailsafeUnaryInterceptor("foghorn", config.Logger),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Foghorn gRPC: %w", err)
	}

	return &GRPCClient{
		conn:    conn,
		clip:    pb.NewClipControlServiceClient(conn),
		dvr:     pb.NewDVRControlServiceClient(conn),
		viewer:  pb.NewViewerControlServiceClient(conn),
		vod:     pb.NewVodControlServiceClient(conn),
		tenant:  pb.NewTenantControlServiceClient(conn),
		logger:  config.Logger,
		timeout: config.Timeout,
	}, nil
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
func (c *GRPCClient) CreateClip(ctx context.Context, req *pb.CreateClipRequest) (*pb.CreateClipResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.clip.CreateClip(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// DeleteClip deletes a clip.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) DeleteClip(ctx context.Context, clipHash string, tenantID *string) (*pb.DeleteClipResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pb.DeleteClipRequest{
		ClipHash: clipHash,
	}
	if tenantID != nil {
		req.TenantId = *tenantID
	}
	var trailers metadata.MD
	resp, err := c.clip.DeleteClip(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// =============================================================================
// DVR OPERATIONS
// =============================================================================

// StartDVR initiates DVR recording for a stream.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.dvr.StartDVR(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// StopDVR stops an active DVR recording.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) StopDVR(ctx context.Context, dvrHash string, tenantID *string, streamID *string) (*pb.StopDVRResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pb.StopDVRRequest{
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

// DeleteDVR deletes a DVR recording and its files.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) DeleteDVR(ctx context.Context, dvrHash string, tenantID *string) (*pb.DeleteDVRResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pb.DeleteDVRRequest{
		DvrHash: dvrHash,
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
func (c *GRPCClient) ResolveViewerEndpoint(ctx context.Context, contentID string, viewerIP *string) (*pb.ViewerEndpointResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pb.ViewerEndpointRequest{
		ContentId: contentID,
		ViewerIp:  viewerIP,
	}
	var trailers metadata.MD
	resp, err := c.viewer.ResolveViewerEndpoint(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// ResolveIngestEndpoint resolves the best ingest endpoint(s) for StreamCrafter.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) ResolveIngestEndpoint(ctx context.Context, streamKey string, viewerIP *string) (*pb.IngestEndpointResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pb.IngestEndpointRequest{
		StreamKey: streamKey,
		ViewerIp:  viewerIP,
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
func (c *GRPCClient) CreateVodUpload(ctx context.Context, req *pb.CreateVodUploadRequest) (*pb.CreateVodUploadResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.CreateVodUpload(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// CompleteVodUpload finalizes a multipart upload after all parts are uploaded.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) CompleteVodUpload(ctx context.Context, req *pb.CompleteVodUploadRequest) (*pb.CompleteVodUploadResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.CompleteVodUpload(ctx, req, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// AbortVodUpload cancels an in-progress multipart upload.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) AbortVodUpload(ctx context.Context, tenantID, uploadID string) (*pb.AbortVodUploadResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.AbortVodUpload(ctx, &pb.AbortVodUploadRequest{
		TenantId: tenantID,
		UploadId: uploadID,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// GetVodAsset returns a single VOD asset by hash.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) GetVodAsset(ctx context.Context, tenantID, artifactHash string) (*pb.VodAssetInfo, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.GetVodAsset(ctx, &pb.GetVodAssetRequest{
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// ListVodAssets returns paginated list of VOD assets for a tenant.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) ListVodAssets(ctx context.Context, tenantID string, pagination *pb.CursorPaginationRequest) (*pb.ListVodAssetsResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.ListVodAssets(ctx, &pb.ListVodAssetsRequest{
		TenantId:   tenantID,
		Pagination: pagination,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// DeleteVodAsset deletes a VOD asset.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) DeleteVodAsset(ctx context.Context, tenantID, artifactHash string) (*pb.DeleteVodAssetResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.vod.DeleteVodAsset(ctx, &pb.DeleteVodAssetRequest{
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// TerminateTenantStreams stops all active streams for a suspended tenant.
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) TerminateTenantStreams(ctx context.Context, tenantID, reason string) (*pb.TerminateTenantStreamsResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.tenant.TerminateTenantStreams(ctx, &pb.TerminateTenantStreamsRequest{
		TenantId: tenantID,
		Reason:   reason,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}

// InvalidateTenantCache clears cached suspension status for a tenant (called on reactivation).
// Returns any trailers emitted by the downstream service.
func (c *GRPCClient) InvalidateTenantCache(ctx context.Context, tenantID, reason string) (*pb.InvalidateTenantCacheResponse, metadata.MD, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var trailers metadata.MD
	resp, err := c.tenant.InvalidateTenantCache(ctx, &pb.InvalidateTenantCacheRequest{
		TenantId: tenantID,
		Reason:   reason,
	}, grpc.Trailer(&trailers))
	return resp, trailers, err
}
