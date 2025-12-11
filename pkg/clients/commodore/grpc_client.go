package commodore

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/cache"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// GRPCClient is the gRPC client for Commodore
type GRPCClient struct {
	conn      *grpc.ClientConn
	internal  pb.InternalServiceClient
	stream    pb.StreamServiceClient
	streamKey pb.StreamKeyServiceClient
	user      pb.UserServiceClient
	developer pb.DeveloperServiceClient
	clip      pb.ClipServiceClient
	dvr       pb.DVRServiceClient
	recording pb.RecordingServiceClient
	viewer    pb.ViewerServiceClient
	logger    logging.Logger
	cache     *cache.Cache
}

// GRPCConfig represents the configuration for the gRPC client
type GRPCConfig struct {
	// GRPCAddr is the gRPC server address (host:port, no scheme)
	GRPCAddr string
	// Timeout for gRPC calls
	Timeout time.Duration
	// Logger for the client
	Logger logging.Logger
	// Cache for caching responses
	Cache *cache.Cache
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

		if userID, ok := ctx.Value("user_id").(string); ok && userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID, ok := ctx.Value("tenant_id").(string); ok && tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}

		// Use user's JWT from context if available, otherwise fall back to service token
		if jwtToken, ok := ctx.Value("jwt_token").(string); ok && jwtToken != "" {
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

// NewGRPCClient creates a new gRPC client for Commodore
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithUnaryInterceptor(authInterceptor(config.ServiceToken)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Commodore gRPC: %w", err)
	}

	return &GRPCClient{
		conn:      conn,
		internal:  pb.NewInternalServiceClient(conn),
		stream:    pb.NewStreamServiceClient(conn),
		streamKey: pb.NewStreamKeyServiceClient(conn),
		user:      pb.NewUserServiceClient(conn),
		developer: pb.NewDeveloperServiceClient(conn),
		clip:      pb.NewClipServiceClient(conn),
		dvr:       pb.NewDVRServiceClient(conn),
		recording: pb.NewRecordingServiceClient(conn),
		viewer:    pb.NewViewerServiceClient(conn),
		logger:    config.Logger,
		cache:     config.Cache,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ============================================================================
// INTERNAL SERVICE OPERATIONS (Foghorn, Sidecar → Commodore)
// ============================================================================

// ValidateStreamKey validates a stream key (called by Foghorn on PUSH_REWRITE)
func (c *GRPCClient) ValidateStreamKey(ctx context.Context, streamKey string) (*pb.ValidateStreamKeyResponse, error) {
	// Check cache first
	if c.cache != nil {
		cacheKey := "commodore:validate:" + streamKey
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.internal.ValidateStreamKey(ctx, &pb.ValidateStreamKeyRequest{
				StreamKey: streamKey,
			})
			if err != nil || !resp.Valid {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*pb.ValidateStreamKeyResponse), nil
		}
	}

	return c.internal.ValidateStreamKey(ctx, &pb.ValidateStreamKeyRequest{
		StreamKey: streamKey,
	})
}

// ResolvePlaybackID resolves a playback ID to internal stream name
func (c *GRPCClient) ResolvePlaybackID(ctx context.Context, playbackID string) (*pb.ResolvePlaybackIDResponse, error) {
	// Check cache first
	if c.cache != nil {
		cacheKey := "commodore:resolve:" + playbackID
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.internal.ResolvePlaybackID(ctx, &pb.ResolvePlaybackIDRequest{
				PlaybackId: playbackID,
			})
			if err != nil {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*pb.ResolvePlaybackIDResponse), nil
		}
	}

	return c.internal.ResolvePlaybackID(ctx, &pb.ResolvePlaybackIDRequest{
		PlaybackId: playbackID,
	})
}

// ResolveInternalName resolves an internal name to tenant context
func (c *GRPCClient) ResolveInternalName(ctx context.Context, internalName string) (*pb.ResolveInternalNameResponse, error) {
	return c.internal.ResolveInternalName(ctx, &pb.ResolveInternalNameRequest{
		InternalName: internalName,
	})
}

// ValidateAPIToken validates a developer API token
func (c *GRPCClient) ValidateAPIToken(ctx context.Context, token string) (*pb.ValidateAPITokenResponse, error) {
	return c.internal.ValidateAPIToken(ctx, &pb.ValidateAPITokenRequest{
		Token: token,
	})
}

// StartDVR initiates DVR recording for a stream (internal, called by Foghorn)
func (c *GRPCClient) StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error) {
	return c.internal.StartDVR(ctx, req)
}

// ============================================================================
// STREAM OPERATIONS
// ============================================================================

// CreateStream creates a new stream
func (c *GRPCClient) CreateStream(ctx context.Context, req *pb.CreateStreamRequest) (*pb.CreateStreamResponse, error) {
	return c.stream.CreateStream(ctx, req)
}

// GetStream gets a stream by ID
func (c *GRPCClient) GetStream(ctx context.Context, streamID string) (*pb.Stream, error) {
	return c.stream.GetStream(ctx, &pb.GetStreamRequest{
		StreamId: streamID,
	})
}

// ListStreams lists streams with cursor pagination
func (c *GRPCClient) ListStreams(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ListStreamsResponse, error) {
	return c.stream.ListStreams(ctx, &pb.ListStreamsRequest{
		Pagination: pagination,
	})
}

// UpdateStream updates a stream
func (c *GRPCClient) UpdateStream(ctx context.Context, req *pb.UpdateStreamRequest) (*pb.Stream, error) {
	return c.stream.UpdateStream(ctx, req)
}

// DeleteStream deletes a stream
func (c *GRPCClient) DeleteStream(ctx context.Context, streamID string) (*pb.DeleteStreamResponse, error) {
	return c.stream.DeleteStream(ctx, &pb.DeleteStreamRequest{
		StreamId: streamID,
	})
}

// RefreshStreamKey refreshes a stream key
func (c *GRPCClient) RefreshStreamKey(ctx context.Context, streamID string) (*pb.RefreshStreamKeyResponse, error) {
	return c.stream.RefreshStreamKey(ctx, &pb.RefreshStreamKeyRequest{
		StreamId: streamID,
	})
}

// ============================================================================
// STREAM KEY OPERATIONS
// ============================================================================

// CreateStreamKey creates a new stream key
func (c *GRPCClient) CreateStreamKey(ctx context.Context, streamID, keyName string) (*pb.StreamKeyResponse, error) {
	return c.streamKey.CreateStreamKey(ctx, &pb.CreateStreamKeyRequest{
		StreamId: streamID,
		KeyName:  keyName,
	})
}

// ListStreamKeys lists stream keys for a stream
func (c *GRPCClient) ListStreamKeys(ctx context.Context, streamID string, pagination *pb.CursorPaginationRequest) (*pb.ListStreamKeysResponse, error) {
	return c.streamKey.ListStreamKeys(ctx, &pb.ListStreamKeysRequest{
		StreamId:   streamID,
		Pagination: pagination,
	})
}

// DeactivateStreamKey deactivates a stream key
func (c *GRPCClient) DeactivateStreamKey(ctx context.Context, streamID, keyID string) error {
	_, err := c.streamKey.DeactivateStreamKey(ctx, &pb.DeactivateStreamKeyRequest{
		StreamId: streamID,
		KeyId:    keyID,
	})
	return err
}

// ============================================================================
// USER OPERATIONS
// ============================================================================

// Login authenticates a user
func (c *GRPCClient) Login(ctx context.Context, req *pb.LoginRequest) (*pb.AuthResponse, error) {
	return c.user.Login(ctx, req)
}

// Register creates a new user
func (c *GRPCClient) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	return c.user.Register(ctx, req)
}

// GetMe gets the current user profile
func (c *GRPCClient) GetMe(ctx context.Context) (*pb.User, error) {
	return c.user.GetMe(ctx, &pb.GetMeRequest{})
}

// Logout logs out a user (invalidates token)
func (c *GRPCClient) Logout(ctx context.Context, token string) (*pb.LogoutResponse, error) {
	return c.user.Logout(ctx, &pb.LogoutRequest{Token: token})
}

// RefreshToken refreshes an authentication token
func (c *GRPCClient) RefreshToken(ctx context.Context, refreshToken string) (*pb.AuthResponse, error) {
	return c.user.RefreshToken(ctx, &pb.RefreshTokenRequest{RefreshToken: refreshToken})
}

// VerifyEmail verifies a user's email with a token
func (c *GRPCClient) VerifyEmail(ctx context.Context, token string) (*pb.VerifyEmailResponse, error) {
	return c.user.VerifyEmail(ctx, &pb.VerifyEmailRequest{Token: token})
}

// ResendVerification resends the email verification link
func (c *GRPCClient) ResendVerification(ctx context.Context, email, turnstileToken string) (*pb.ResendVerificationResponse, error) {
	return c.user.ResendVerification(ctx, &pb.ResendVerificationRequest{
		Email:          email,
		TurnstileToken: turnstileToken,
	})
}

// ForgotPassword initiates password reset flow
func (c *GRPCClient) ForgotPassword(ctx context.Context, email string) (*pb.ForgotPasswordResponse, error) {
	return c.user.ForgotPassword(ctx, &pb.ForgotPasswordRequest{Email: email})
}

// ResetPassword resets a user's password with a token
func (c *GRPCClient) ResetPassword(ctx context.Context, token, password string) (*pb.ResetPasswordResponse, error) {
	return c.user.ResetPassword(ctx, &pb.ResetPasswordRequest{Token: token, Password: password})
}

// UpdateMe updates the current user's profile
func (c *GRPCClient) UpdateMe(ctx context.Context, req *pb.UpdateMeRequest) (*pb.User, error) {
	return c.user.UpdateMe(ctx, req)
}

// UpdateNewsletter updates the user's newsletter subscription
func (c *GRPCClient) UpdateNewsletter(ctx context.Context, subscribed bool) (*pb.UpdateNewsletterResponse, error) {
	return c.user.UpdateNewsletter(ctx, &pb.UpdateNewsletterRequest{Subscribed: subscribed})
}

// ============================================================================
// DEVELOPER/API TOKEN OPERATIONS
// ============================================================================

// CreateAPIToken creates a new API token
func (c *GRPCClient) CreateAPIToken(ctx context.Context, req *pb.CreateAPITokenRequest) (*pb.CreateAPITokenResponse, error) {
	return c.developer.CreateAPIToken(ctx, req)
}

// ListAPITokens lists API tokens
func (c *GRPCClient) ListAPITokens(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ListAPITokensResponse, error) {
	return c.developer.ListAPITokens(ctx, &pb.ListAPITokensRequest{
		Pagination: pagination,
	})
}

// RevokeAPIToken revokes an API token
func (c *GRPCClient) RevokeAPIToken(ctx context.Context, tokenID string) (*pb.RevokeAPITokenResponse, error) {
	return c.developer.RevokeAPIToken(ctx, &pb.RevokeAPITokenRequest{
		TokenId: tokenID,
	})
}

// ============================================================================
// CLIP OPERATIONS (Gateway → Commodore → Foghorn proxy)
// ============================================================================

// CreateClip creates a new clip
func (c *GRPCClient) CreateClip(ctx context.Context, req *pb.CreateClipRequest) (*pb.CreateClipResponse, error) {
	return c.clip.CreateClip(ctx, req)
}

// GetClips lists clips with optional internal_name filter
func (c *GRPCClient) GetClips(ctx context.Context, tenantID string, internalName *string, pagination *pb.CursorPaginationRequest) (*pb.GetClipsResponse, error) {
	req := &pb.GetClipsRequest{
		TenantId:   tenantID,
		Pagination: pagination,
	}
	if internalName != nil {
		req.InternalName = internalName
	}
	return c.clip.GetClips(ctx, req)
}

// GetClip gets a single clip by hash
func (c *GRPCClient) GetClip(ctx context.Context, clipHash string) (*pb.ClipInfo, error) {
	return c.clip.GetClip(ctx, &pb.GetClipRequest{
		ClipHash: clipHash,
	})
}

// GetClipURLs gets viewing URLs for a clip
func (c *GRPCClient) GetClipURLs(ctx context.Context, clipHash string) (*pb.ClipViewingURLs, error) {
	return c.clip.GetClipURLs(ctx, &pb.GetClipURLsRequest{
		ClipHash: clipHash,
	})
}

// DeleteClip deletes a clip
func (c *GRPCClient) DeleteClip(ctx context.Context, clipHash string) error {
	_, err := c.clip.DeleteClip(ctx, &pb.DeleteClipRequest{
		ClipHash: clipHash,
	})
	return err
}

// ============================================================================
// DVR OPERATIONS (Gateway → Commodore → Foghorn proxy)
// ============================================================================

// StopDVR stops a DVR recording
func (c *GRPCClient) StopDVR(ctx context.Context, dvrHash string) error {
	_, err := c.dvr.StopDVR(ctx, &pb.StopDVRRequest{
		DvrHash: dvrHash,
	})
	return err
}

// ListDVRRequests lists DVR recordings with filters
func (c *GRPCClient) ListDVRRequests(ctx context.Context, tenantID string, internalName *string, pagination *pb.CursorPaginationRequest) (*pb.ListDVRRecordingsResponse, error) {
	req := &pb.ListDVRRecordingsRequest{
		TenantId:   tenantID,
		Pagination: pagination,
	}
	if internalName != nil {
		req.InternalName = internalName
	}
	return c.dvr.ListDVRRequests(ctx, req)
}

// GetDVRStatus gets the status of a DVR recording
func (c *GRPCClient) GetDVRStatus(ctx context.Context, dvrHash string) (*pb.DVRInfo, error) {
	return c.dvr.GetDVRStatus(ctx, &pb.GetDVRStatusRequest{
		DvrHash: dvrHash,
	})
}

// ============================================================================
// RECORDING OPERATIONS (Gateway → Commodore)
// ============================================================================

// ListRecordings lists recordings with optional stream filter
func (c *GRPCClient) ListRecordings(ctx context.Context, streamID string, pagination *pb.CursorPaginationRequest) (*pb.ListRecordingsResponse, error) {
	return c.recording.ListRecordings(ctx, &pb.ListRecordingsRequest{
		StreamId:   streamID,
		Pagination: pagination,
	})
}

// ============================================================================
// VIEWER OPERATIONS (Gateway/Player → Commodore → Foghorn proxy)
// ============================================================================

// ResolveViewerEndpoint resolves the best endpoint for a viewer
func (c *GRPCClient) ResolveViewerEndpoint(ctx context.Context, contentType, contentID, viewerIP string) (*pb.ViewerEndpointResponse, error) {
	req := &pb.ViewerEndpointRequest{
		ContentType: contentType,
		ContentId:   contentID,
	}
	if viewerIP != "" {
		req.ViewerIp = &viewerIP
	}
	return c.viewer.ResolveViewerEndpoint(ctx, req)
}

// GetStreamMeta gets stream metadata from MistServer
func (c *GRPCClient) GetStreamMeta(ctx context.Context, internalName, contentType string, includeRaw bool, targetNodeID, targetBaseURL string) (*pb.StreamMetaResponse, error) {
	req := &pb.StreamMetaRequest{
		InternalName: internalName,
		IncludeRaw:   includeRaw,
	}
	if contentType != "" {
		req.ContentType = &contentType
	}
	if targetNodeID != "" {
		req.TargetNodeId = &targetNodeID
	}
	if targetBaseURL != "" {
		req.TargetBaseUrl = &targetBaseURL
	}
	return c.viewer.GetStreamMeta(ctx, req)
}
