package commodore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const DefaultServerName = "commodore.internal"

// GRPCClient is the gRPC client for Commodore
type GRPCClient struct {
	conn         *grpc.ClientConn
	internal     pb.InternalServiceClient
	stream       pb.StreamServiceClient
	streamKey    pb.StreamKeyServiceClient
	user         pb.UserServiceClient
	developer    pb.DeveloperServiceClient
	clip         pb.ClipServiceClient
	dvr          pb.DVRServiceClient
	viewer       pb.ViewerServiceClient
	vod          pb.VodServiceClient
	nodeMgmt     pb.NodeManagementServiceClient
	pushTarget   pb.PushTargetServiceClient
	playbackAuth pb.PlaybackAccessControlServiceClient
	logger       logging.Logger
	cache        *cache.Cache
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
	// TLS configuration for the gRPC connection.
	AllowInsecure bool
	CACertFile    string
	CACertPEM     string
	ServerName    string
}

// authInterceptor propagates authentication to gRPC metadata.
// This reads user_id, tenant_id, and jwt_token from the Go context (set by Gateway middleware)
// and adds them to outgoing gRPC metadata for downstream services.
// If no user JWT is available, it falls back to the service token for service-to-service calls.
func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Extract user context from Go context and add to gRPC metadata
		md := metadata.MD{}

		if userID := ctxkeys.GetUserID(ctx); userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}
		if ctxkeys.IsDemoMode(ctx) {
			md.Set("x-demo-mode", "true")
		}

		// Use user's JWT from context if available, otherwise fall back to service token
		if jwtToken := ctxkeys.GetJWTToken(ctx); jwtToken != "" {
			md.Set("authorization", "Bearer "+jwtToken)
		} else if serviceToken != "" {
			md.Set("authorization", "Bearer "+serviceToken)
		}

		// Forward X-PAYMENT header for x402 settlement (viewer-pays flows)
		if xPayment, ok := ctx.Value(ctxkeys.KeyXPayment).(string); ok && xPayment != "" {
			md.Set("x-payment", xPayment)
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

	tlsCfg := grpcutil.ClientTLSConfig{
		CACertFile:        config.CACertFile,
		CACertPEM:         config.CACertPEM,
		ServerName:        config.ServerName,
		DefaultServerName: DefaultServerName,
		AllowInsecure:     config.AllowInsecure,
	}
	transport, err := grpcutil.ClientTLS(tlsCfg, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("configure Commodore gRPC TLS: %w", err)
	}

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		transport,
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithChainUnaryInterceptor(
			authInterceptor(config.ServiceToken),
			clients.FailsafeUnaryInterceptor("commodore", config.Logger),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Commodore gRPC: %w", err)
	}

	return &GRPCClient{
		conn:         conn,
		internal:     pb.NewInternalServiceClient(conn),
		stream:       pb.NewStreamServiceClient(conn),
		streamKey:    pb.NewStreamKeyServiceClient(conn),
		user:         pb.NewUserServiceClient(conn),
		developer:    pb.NewDeveloperServiceClient(conn),
		clip:         pb.NewClipServiceClient(conn),
		dvr:          pb.NewDVRServiceClient(conn),
		viewer:       pb.NewViewerServiceClient(conn),
		vod:          pb.NewVodServiceClient(conn),
		nodeMgmt:     pb.NewNodeManagementServiceClient(conn),
		pushTarget:   pb.NewPushTargetServiceClient(conn),
		playbackAuth: pb.NewPlaybackAccessControlServiceClient(conn),
		logger:       config.Logger,
		cache:        config.Cache,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// InvalidateTenantCacheKeys removes tenant-scoped Commodore resolver entries.
func (c *GRPCClient) InvalidateTenantCacheKeys(tenantID string) {
	if c.cache == nil || tenantID == "" {
		return
	}
	for _, entry := range c.cache.Snapshot() {
		if strings.HasPrefix(entry.Key, tenantID+":") {
			c.cache.Delete(entry.Key)
		}
	}
}

func buildValidateStreamKeyCacheKey(streamKey, clusterID string) string {
	cacheKey := "commodore:validate:" + streamKey
	if clusterID == "" {
		return cacheKey
	}
	return cacheKey + ":cluster:" + clusterID
}

// ============================================================================
// INTERNAL SERVICE OPERATIONS (Foghorn, Sidecar → Commodore)
// ============================================================================

// ValidateStreamKey validates a stream key (called by Foghorn on PUSH_REWRITE).
// clusterID is optional — when provided, Commodore records which cluster the stream is ingesting on.
func (c *GRPCClient) ValidateStreamKey(ctx context.Context, streamKey string, clusterID ...string) (*pb.ValidateStreamKeyResponse, error) {
	cid := ""
	if len(clusterID) > 0 {
		cid = clusterID[0]
	}
	cacheKey := buildValidateStreamKeyCacheKey(streamKey, cid)
	resp, err := c.internal.ValidateStreamKey(ctx, &pb.ValidateStreamKeyRequest{
		StreamKey: streamKey,
		ClusterId: cid,
	})
	if err == nil {
		if c.cache != nil && resp != nil && resp.GetValid() {
			c.cache.SetDefault(cacheKey, resp)
		}
		return resp, nil
	}

	if c.cache != nil {
		if cached, ok := c.cache.Peek(cacheKey); ok {
			if cachedResp, ok := cached.(*pb.ValidateStreamKeyResponse); ok && cachedResp.GetValid() {
				return cachedResp, nil
			}
		}
	}

	return nil, err
}

// ResolvePlaybackID resolves a playback ID to internal stream name
func (c *GRPCClient) ResolvePlaybackID(ctx context.Context, playbackID string) (*pb.ResolvePlaybackIDResponse, error) {
	// Check cache first
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:resolve:" + playbackID
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolvePlaybackID(ctx, &pb.ResolvePlaybackIDRequest{
					PlaybackId: playbackID,
				})
				if err != nil {
					return nil, false, err
				}
				return resp, true, nil
			}); ok {
				return v.(*pb.ResolvePlaybackIDResponse), nil //nolint:errcheck // type guaranteed by cache
			}
		}
	}

	return c.internal.ResolvePlaybackID(ctx, &pb.ResolvePlaybackIDRequest{
		PlaybackId: playbackID,
	})
}

// ResolveInternalName resolves an internal name to tenant context
func (c *GRPCClient) ResolveInternalName(ctx context.Context, internalName string) (*pb.ResolveInternalNameResponse, error) {
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:internal:" + internalName
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolveInternalName(ctx, &pb.ResolveInternalNameRequest{
					InternalName: internalName,
				})
				if err != nil {
					return nil, false, err
				}
				return resp, true, nil
			}); ok {
				if v == nil {
					return nil, fmt.Errorf("cached ResolveInternalName response is nil")
				}
				resp, ok := v.(*pb.ResolveInternalNameResponse)
				if !ok {
					return nil, fmt.Errorf("cached ResolveInternalName response has unexpected type %T", v)
				}
				return resp, nil
			}
		}
	}

	return c.internal.ResolveInternalName(ctx, &pb.ResolveInternalNameRequest{
		InternalName: internalName,
	})
}

// ResolveArtifactPlaybackID resolves an artifact playback ID to artifact identity
func (c *GRPCClient) ResolveArtifactPlaybackID(ctx context.Context, playbackID string) (*pb.ResolveArtifactPlaybackIDResponse, error) {
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:artifact:playback:" + playbackID
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolveArtifactPlaybackID(ctx, &pb.ResolveArtifactPlaybackIDRequest{
					PlaybackId: playbackID,
				})
				if err != nil || !resp.Found {
					return nil, false, err
				}
				return resp, true, nil
			}); ok {
				return v.(*pb.ResolveArtifactPlaybackIDResponse), nil //nolint:errcheck // type guaranteed by cache
			}
		}
	}

	return c.internal.ResolveArtifactPlaybackID(ctx, &pb.ResolveArtifactPlaybackIDRequest{
		PlaybackId: playbackID,
	})
}

// ResolveArtifactInternalName resolves an artifact internal routing name to artifact identity
func (c *GRPCClient) ResolveArtifactInternalName(ctx context.Context, internalName string) (*pb.ResolveArtifactInternalNameResponse, error) {
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:artifact:internal:" + internalName
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolveArtifactInternalName(ctx, &pb.ResolveArtifactInternalNameRequest{
					InternalName: internalName,
				})
				if err != nil || !resp.Found {
					return nil, false, err
				}
				return resp, true, nil
			}); ok {
				return v.(*pb.ResolveArtifactInternalNameResponse), nil //nolint:errcheck // type guaranteed by cache
			}
		}
	}

	return c.internal.ResolveArtifactInternalName(ctx, &pb.ResolveArtifactInternalNameRequest{
		InternalName: internalName,
	})
}

// ResolvePullSourceByInternalName returns the configured upstream pull URI for a
// pull-mode stream. Used by Foghorn STREAM_SOURCE handling and /source origin
// selection. No tenant-scoped caching here — the value is tenant-attributed in
// the response and Foghorn caches per process if needed.
func (c *GRPCClient) ResolvePullSourceByInternalName(ctx context.Context, internalName string) (*pb.ResolvePullSourceByInternalNameResponse, error) {
	return c.internal.ResolvePullSourceByInternalName(ctx, &pb.ResolvePullSourceByInternalNameRequest{
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
// CLIP/DVR REGISTRY (Foghorn → Commodore)
// Business registry for clips and DVR recordings.
// See: docs/architecture/clips-dvr.md
// ============================================================================

// RegisterClip registers a new clip in the business registry
// Called by Foghorn during the CreateClip flow
func (c *GRPCClient) RegisterClip(ctx context.Context, req *pb.RegisterClipRequest) (*pb.RegisterClipResponse, error) {
	return c.internal.RegisterClip(ctx, req)
}

// RegisterDVR registers a new DVR recording in the business registry
// Called by Foghorn during the StartDVR flow
func (c *GRPCClient) RegisterDVR(ctx context.Context, req *pb.RegisterDVRRequest) (*pb.RegisterDVRResponse, error) {
	return c.internal.RegisterDVR(ctx, req)
}

// MarkArtifactThumbnailsReady flips has_thumbnails=TRUE and stamps
// storage_cluster_id on the commodore.{clips, dvr_recordings, vod_assets}
// row matching (tenant_id, asset_key). Idempotent. Called from Foghorn's
// processThumbnailUploaded confirmation site.
func (c *GRPCClient) MarkArtifactThumbnailsReady(ctx context.Context, tenantID string, assetType pb.ArtifactAssetType, assetKey, storageClusterID string) (*pb.MarkArtifactThumbnailsReadyResponse, error) {
	return c.internal.MarkArtifactThumbnailsReady(ctx, &pb.MarkArtifactThumbnailsReadyRequest{
		TenantId:         tenantID,
		AssetType:        assetType,
		AssetKey:         assetKey,
		StorageClusterId: storageClusterID,
	})
}

// UpdateArtifactStorageCluster updates storage_cluster_id only — never
// touches has_thumbnails. Called whenever Foghorn mutates
// foghorn.artifacts.storage_cluster_id.
func (c *GRPCClient) UpdateArtifactStorageCluster(ctx context.Context, tenantID string, assetType pb.ArtifactAssetType, assetKey, storageClusterID string) (*pb.UpdateArtifactStorageClusterResponse, error) {
	return c.internal.UpdateArtifactStorageCluster(ctx, &pb.UpdateArtifactStorageClusterRequest{
		TenantId:         tenantID,
		AssetType:        assetType,
		AssetKey:         assetKey,
		StorageClusterId: storageClusterID,
	})
}

// UpdateArtifactSize projects Foghorn's authoritative artifact byte count into
// the Commodore registry row used for catalog pagination and sorting.
func (c *GRPCClient) UpdateArtifactSize(ctx context.Context, tenantID string, assetType pb.ArtifactAssetType, assetKey string, sizeBytes int64) (*pb.UpdateArtifactSizeResponse, error) {
	return c.internal.UpdateArtifactSize(ctx, &pb.UpdateArtifactSizeRequest{
		TenantId:  tenantID,
		AssetType: assetType,
		AssetKey:  assetKey,
		SizeBytes: sizeBytes,
	})
}

// UpdateDVRRetention back-fills retention_until on a finalized DVR.
// Foghorn computes retention_until = ended_at + dvr_retention_days*24h
// from the persisted policy snapshot at FinalizeDVR time and pushes it
// here so commodore.dvr_recordings.retention_until reflects post-end
// retention. Active recordings carry NULL until they finalize.
func (c *GRPCClient) UpdateDVRRetention(ctx context.Context, req *pb.UpdateDVRRetentionRequest) (*pb.UpdateDVRRetentionResponse, error) {
	return c.internal.UpdateDVRRetention(ctx, req)
}

// ============================================================================
// MEDIA RETENTION POLICY (Bridge → Commodore)
// Customer-tunable retention defaults + per-asset overrides.
// ============================================================================

func (c *GRPCClient) GetMediaRetentionPolicy(ctx context.Context, req *pb.GetMediaRetentionPolicyRequest) (*pb.GetMediaRetentionPolicyResponse, error) {
	return c.internal.GetMediaRetentionPolicy(ctx, req)
}

func (c *GRPCClient) SetMediaRetentionPolicy(ctx context.Context, req *pb.SetMediaRetentionPolicyRequest) (*pb.SetMediaRetentionPolicyResponse, error) {
	return c.internal.SetMediaRetentionPolicy(ctx, req)
}

func (c *GRPCClient) UpdateAssetRetention(ctx context.Context, req *pb.UpdateAssetRetentionRequest) (*pb.UpdateAssetRetentionResponse, error) {
	return c.internal.UpdateAssetRetention(ctx, req)
}

func (c *GRPCClient) ResetAssetRetention(ctx context.Context, req *pb.ResetAssetRetentionRequest) (*pb.UpdateAssetRetentionResponse, error) {
	return c.internal.ResetAssetRetention(ctx, req)
}

// SetStreamRetentionOverrides writes per-stream DVR/clip retention overrides.
// Commodore clamps positive values down to the tier cap and treats -1 as a
// clear sentinel; the response reports the resolved post-write state.
func (c *GRPCClient) SetStreamRetentionOverrides(ctx context.Context, req *pb.SetStreamRetentionOverridesRequest) (*pb.SetStreamRetentionOverridesResponse, error) {
	return c.internal.SetStreamRetentionOverrides(ctx, req)
}

// TestPlaybackAccess facades Foghorn's dry-run evaluator. Webhook mode can
// take up to ~10s for the customer endpoint to respond — keep timeouts
// generous on the caller side.
func (c *GRPCClient) TestPlaybackAccess(ctx context.Context, req *pb.TestPlaybackAccessRequest) (*pb.TestPlaybackAccessResponse, error) {
	return c.internal.TestPlaybackAccess(ctx, req)
}

// RecordPullSourceEvent appends a row to commodore.pull_source_events. Used
// by Foghorn's STREAM_SOURCE handler to audit pull resolution outcomes.
func (c *GRPCClient) RecordPullSourceEvent(ctx context.Context, req *pb.RecordPullSourceEventRequest) error {
	_, err := c.internal.RecordPullSourceEvent(ctx, req)
	return err
}

// ListPullSourceEvents returns the most recent N pull-source resolution
// events for a stream. Tenant-scoped via the JWT.
func (c *GRPCClient) ListPullSourceEvents(ctx context.Context, req *pb.ListPullSourceEventsRequest) (*pb.ListPullSourceEventsResponse, error) {
	return c.internal.ListPullSourceEvents(ctx, req)
}

// ResolveClipHash resolves a clip hash to tenant context
// Used for analytics enrichment and playback authorization
func (c *GRPCClient) ResolveClipHash(ctx context.Context, clipHash string) (*pb.ResolveClipHashResponse, error) {
	// Use cache for context lookups (high frequency during playback/events)
	if c.cache != nil {
		cacheKey := "commodore:clip:" + clipHash
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.internal.ResolveClipHash(ctx, &pb.ResolveClipHashRequest{
				ClipHash: clipHash,
			})
			if err != nil || !resp.Found {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*pb.ResolveClipHashResponse), nil //nolint:errcheck // type guaranteed by cache
		}
	}

	return c.internal.ResolveClipHash(ctx, &pb.ResolveClipHashRequest{
		ClipHash: clipHash,
	})
}

// ResolveDVRHash resolves a DVR hash to tenant context
// Used for analytics enrichment and playback authorization
func (c *GRPCClient) ResolveDVRHash(ctx context.Context, dvrHash string) (*pb.ResolveDVRHashResponse, error) {
	// Use cache for context lookups (high frequency during playback/events)
	if c.cache != nil {
		cacheKey := "commodore:dvr:" + dvrHash
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.internal.ResolveDVRHash(ctx, &pb.ResolveDVRHashRequest{
				DvrHash: dvrHash,
			})
			if err != nil || !resp.Found {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*pb.ResolveDVRHashResponse), nil //nolint:errcheck // type guaranteed by cache
		}
	}

	return c.internal.ResolveDVRHash(ctx, &pb.ResolveDVRHashRequest{
		DvrHash: dvrHash,
	})
}

// ResolveIdentifier provides unified resolution across all Commodore registries
// Checks: streams (internal_name), streams (playback_id), clips, DVR, VOD
// Used by Foghorn for analytics enrichment when local state cache misses
func (c *GRPCClient) ResolveIdentifier(ctx context.Context, identifier string) (*pb.ResolveIdentifierResponse, error) {
	// Use cache for context lookups (high frequency during playback/events)
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:id:" + identifier
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolveIdentifier(ctx, &pb.ResolveIdentifierRequest{
					Identifier: identifier,
				})
				if err != nil || !resp.Found {
					return nil, false, err
				}
				return resp, true, nil
			}); ok {
				return v.(*pb.ResolveIdentifierResponse), nil //nolint:errcheck // type guaranteed by cache
			}
		}
	}

	return c.internal.ResolveIdentifier(ctx, &pb.ResolveIdentifierRequest{
		Identifier: identifier,
	})
}

// RegisterVod registers a new VOD asset in the business registry
// Called by Foghorn during CreateVodUpload flow (mirrors DVR/clip pattern)
func (c *GRPCClient) RegisterVod(ctx context.Context, tenantID, userID, filename string, title, description, contentType *string, sizeBytes *int64) (*pb.RegisterVodResponse, error) {
	req := &pb.RegisterVodRequest{
		TenantId: tenantID,
		UserId:   userID,
		Filename: filename,
	}
	if title != nil {
		req.Title = title
	}
	if description != nil {
		req.Description = description
	}
	if contentType != nil {
		req.ContentType = contentType
	}
	if sizeBytes != nil {
		req.SizeBytes = sizeBytes
	}
	return c.internal.RegisterVod(ctx, req)
}

// ResolveVodHash resolves a VOD hash to tenant context
// Used for analytics enrichment, playback authorization, and lifecycle operations
func (c *GRPCClient) ResolveVodHash(ctx context.Context, vodHash string) (*pb.ResolveVodHashResponse, error) {
	// Use cache for context lookups (high frequency during playback/events)
	if c.cache != nil {
		cacheKey := "commodore:vod:" + vodHash
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.internal.ResolveVodHash(ctx, &pb.ResolveVodHashRequest{
				VodHash: vodHash,
			})
			if err != nil || !resp.Found {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*pb.ResolveVodHashResponse), nil //nolint:errcheck // type guaranteed by cache
		}
	}

	return c.internal.ResolveVodHash(ctx, &pb.ResolveVodHashRequest{
		VodHash: vodHash,
	})
}

// ResolveVodID resolves a VOD relay ID (vod_assets.id) to VOD hash + tenant context
func (c *GRPCClient) ResolveVodID(ctx context.Context, vodID string) (*pb.ResolveVodIDResponse, error) {
	return c.internal.ResolveVodID(ctx, &pb.ResolveVodIDRequest{
		VodId: vodID,
	})
}

// MintChapterPlaybackID asks Commodore to mint (or return the existing)
// public playback_id for a hidden chapter artifact. Idempotent on
// chapter_id.
func (c *GRPCClient) MintChapterPlaybackID(ctx context.Context, chapterID, tenantID, artifactHash, userID, filename, originClusterID, storageClusterID, streamID string) (*pb.MintChapterPlaybackIDResponse, error) {
	return c.internal.MintChapterPlaybackID(ctx, &pb.MintChapterPlaybackIDRequest{
		ChapterId:        chapterID,
		TenantId:         tenantID,
		ArtifactHash:     artifactHash,
		UserId:           userID,
		Filename:         filename,
		OriginClusterId:  originClusterID,
		StorageClusterId: storageClusterID,
		StreamId:         streamID,
	})
}

// GetTenantProcessesJSON returns the tenant's MistServer process
// config JSON for the given stream type ("live" | "vod"). Callers may
// filter the returned config for live-derived asset pipelines.
func (c *GRPCClient) GetTenantProcessesJSON(ctx context.Context, tenantID, streamType, clusterID string) (*pb.GetTenantProcessesJSONResponse, error) {
	return c.internal.GetTenantProcessesJSON(ctx, &pb.GetTenantProcessesJSONRequest{
		TenantId:   tenantID,
		StreamType: streamType,
		ClusterId:  clusterID,
	})
}

// ResolveChapterPlaybackID maps a public chapter playback_id back to its
// internal identity (chapter_id, tenant_id, artifact_hash). Cached
// because chapter playback lookups happen on every viewer connect.
func (c *GRPCClient) ResolveChapterPlaybackID(ctx context.Context, playbackID string) (*pb.ResolveChapterPlaybackIDResponse, error) {
	if c.cache != nil {
		cacheKey := "commodore:chapter_pb:" + playbackID
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) { //nolint:errcheck // miss is recovered by the direct call below
			resp, err := c.internal.ResolveChapterPlaybackID(ctx, &pb.ResolveChapterPlaybackIDRequest{
				PlaybackId: playbackID,
			})
			if err != nil || !resp.GetFound() {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*pb.ResolveChapterPlaybackIDResponse), nil //nolint:errcheck // type guaranteed by cache
		}
	}
	return c.internal.ResolveChapterPlaybackID(ctx, &pb.ResolveChapterPlaybackIDRequest{
		PlaybackId: playbackID,
	})
}

// ============================================================================
// WALLET IDENTITY (x402 / Agent Access)
// ============================================================================

// GetOrCreateWalletUser looks up or creates a tenant/user for a verified wallet address.
// This is called by x402 middleware after verifying the ERC-3009 payment signature.
// If the wallet is unknown, Commodore creates: tenant (prepaid) + user (email=NULL) + wallet_identity.
func (c *GRPCClient) GetOrCreateWalletUser(ctx context.Context, chainType, walletAddress string) (*pb.GetOrCreateWalletUserResponse, error) {
	return c.internal.GetOrCreateWalletUser(ctx, &pb.GetOrCreateWalletUserRequest{
		ChainType:     chainType,
		WalletAddress: walletAddress,
	})
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

// GetStreamsBatch fetches multiple streams by IDs in a single batch call
func (c *GRPCClient) GetStreamsBatch(ctx context.Context, streamIDs []string) (*pb.GetStreamsBatchResponse, error) {
	return c.stream.GetStreamsBatch(ctx, &pb.GetStreamsBatchRequest{StreamIds: streamIDs})
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

// GetNewsletterStatus returns the user's current newsletter subscription status
func (c *GRPCClient) GetNewsletterStatus(ctx context.Context) (bool, error) {
	resp, err := c.user.GetNewsletterStatus(ctx, &pb.GetNewsletterStatusRequest{})
	if err != nil {
		return false, err
	}
	return resp.Subscribed, nil
}

// ============================================================================
// WALLET AUTHENTICATION OPERATIONS
// ============================================================================

// WalletLogin authenticates a user via wallet signature, auto-provisioning if new
func (c *GRPCClient) WalletLogin(ctx context.Context, address, message, signature string, attribution *pb.SignupAttribution) (*pb.AuthResponse, error) {
	return c.user.WalletLogin(ctx, &pb.WalletLoginRequest{
		WalletAddress: address,
		Message:       message,
		Signature:     signature,
		Attribution:   attribution,
	})
}

// WalletLoginWithX402 authenticates via x402 payload and returns session token + payment info.
func (c *GRPCClient) WalletLoginWithX402(ctx context.Context, payment *pb.X402PaymentPayload, clientIP, targetTenantID string, attribution *pb.SignupAttribution) (*pb.WalletLoginWithX402Response, error) {
	req := &pb.WalletLoginWithX402Request{
		Payment:     payment,
		ClientIp:    clientIP,
		Attribution: attribution,
	}
	if targetTenantID != "" {
		req.TargetTenantId = &targetTenantID
	}
	return c.user.WalletLoginWithX402(ctx, req)
}

// LinkWallet links a wallet to the current user's account
func (c *GRPCClient) LinkWallet(ctx context.Context, address, message, signature string) (*pb.WalletIdentity, error) {
	return c.user.LinkWallet(ctx, &pb.LinkWalletRequest{
		WalletAddress: address,
		Message:       message,
		Signature:     signature,
	})
}

// UnlinkWallet removes a wallet from the current user's account
func (c *GRPCClient) UnlinkWallet(ctx context.Context, walletID string) (*pb.UnlinkWalletResponse, error) {
	return c.user.UnlinkWallet(ctx, &pb.UnlinkWalletRequest{
		WalletId: walletID,
	})
}

// ListWallets lists wallets linked to the current user
func (c *GRPCClient) ListWallets(ctx context.Context) (*pb.ListWalletsResponse, error) {
	return c.user.ListWallets(ctx, &pb.ListWalletsRequest{})
}

// LinkEmail adds an email to a wallet-only account (for postpaid upgrade path)
func (c *GRPCClient) LinkEmail(ctx context.Context, email, password string) (*pb.LinkEmailResponse, error) {
	return c.user.LinkEmail(ctx, &pb.LinkEmailRequest{
		Email:    email,
		Password: password,
	})
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

type MediaListOptions struct {
	Search        string
	SortField     string
	SortDirection string
	Offset        *int32
}

// CreateClip creates a new clip
func (c *GRPCClient) CreateClip(ctx context.Context, req *pb.CreateClipRequest) (*pb.CreateClipResponse, error) {
	return c.clip.CreateClip(ctx, req)
}

// GetClips lists clips with optional stream_id filter
func (c *GRPCClient) GetClips(ctx context.Context, tenantID string, streamID *string, pagination *pb.CursorPaginationRequest, opts ...MediaListOptions) (*pb.GetClipsResponse, error) {
	req := &pb.GetClipsRequest{
		TenantId:   tenantID,
		Pagination: pagination,
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	applyMediaListOptionsToClips(req, opts...)
	return c.clip.GetClips(ctx, req)
}

// GetClip gets a single clip by hash
func (c *GRPCClient) GetClip(ctx context.Context, clipHash string) (*pb.ClipInfo, error) {
	return c.clip.GetClip(ctx, &pb.GetClipRequest{
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

// DeleteDVR deletes a DVR recording
func (c *GRPCClient) DeleteDVR(ctx context.Context, dvrHash string) error {
	_, err := c.dvr.DeleteDVR(ctx, &pb.DeleteDVRRequest{
		DvrHash: dvrHash,
	})
	return err
}

// RetrieveDVRChapter returns chapter metadata (state, range, public
// playback_id) for a single chapter. Customer-facing path:
// api_gateway → Commodore → Foghorn.
func (c *GRPCClient) RetrieveDVRChapter(ctx context.Context, req *pb.RetrieveDVRChapterRequest) (*pb.RetrieveDVRChapterResponse, error) {
	return c.internal.RetrieveDVRChapter(ctx, req)
}

// ListDVRChapters paginates chapter rows for player navigation.
func (c *GRPCClient) ListDVRChapters(ctx context.Context, req *pb.ListDVRChaptersRequest) (*pb.ListDVRChaptersResponse, error) {
	return c.internal.ListDVRChapters(ctx, req)
}

// ListStorageArtifacts returns the unified account storage browser projection.
func (c *GRPCClient) ListStorageArtifacts(ctx context.Context, req *pb.ListStorageArtifactsRequest) (*pb.ListStorageArtifactsResponse, error) {
	return c.internal.ListStorageArtifacts(ctx, req)
}

// ListDVRRequests lists DVR recordings with filters
func (c *GRPCClient) ListDVRRequests(ctx context.Context, tenantID string, streamID *string, pagination *pb.CursorPaginationRequest, opts ...MediaListOptions) (*pb.ListDVRRecordingsResponse, error) {
	req := &pb.ListDVRRecordingsRequest{
		TenantId:   tenantID,
		Pagination: pagination,
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	applyMediaListOptionsToDVR(req, opts...)
	return c.dvr.ListDVRRequests(ctx, req)
}

// ============================================================================
// VIEWER OPERATIONS (Gateway/Player → Commodore → Foghorn proxy)
// ============================================================================

// ResolveViewerEndpoint resolves the best endpoint for a viewer
func (c *GRPCClient) ResolveViewerEndpoint(ctx context.Context, contentID, viewerIP, viewerToken string) (*pb.ViewerEndpointResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("CRITICAL: Commodore GRPCClient is nil")
	}
	if c.viewer == nil {
		return nil, fmt.Errorf("CRITICAL: Commodore.viewer client is nil - gRPC connection failed or not initialized?")
	}
	req := &pb.ViewerEndpointRequest{
		ContentId: contentID,
	}
	if viewerIP != "" {
		req.ViewerIp = &viewerIP
	}
	if viewerToken != "" {
		req.ViewerToken = &viewerToken
	}
	return c.viewer.ResolveViewerEndpoint(ctx, req)
}

// ResolveIngestEndpoint resolves the best ingest endpoint for StreamCrafter
func (c *GRPCClient) ResolveIngestEndpoint(ctx context.Context, streamKey, viewerIP string) (*pb.IngestEndpointResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("CRITICAL: Commodore GRPCClient is nil")
	}
	if c.viewer == nil {
		return nil, fmt.Errorf("CRITICAL: Commodore.viewer client is nil - gRPC connection failed or not initialized?")
	}
	req := &pb.IngestEndpointRequest{
		StreamKey: streamKey,
	}
	if viewerIP != "" {
		req.ViewerIp = &viewerIP
	}
	return c.viewer.ResolveIngestEndpoint(ctx, req)
}

// ============================================================================
// VOD OPERATIONS (Gateway → Commodore → Foghorn proxy)
// User-initiated video uploads (distinct from clips/DVR which are stream-derived)
// ============================================================================

// CreateVodUpload initiates a multipart upload for a VOD asset
func (c *GRPCClient) CreateVodUpload(ctx context.Context, req *pb.CreateVodUploadRequest) (*pb.CreateVodUploadResponse, error) {
	return c.vod.CreateVodUpload(ctx, req)
}

// CompleteVodUpload finalizes a multipart upload after all parts are uploaded
func (c *GRPCClient) CompleteVodUpload(ctx context.Context, req *pb.CompleteVodUploadRequest) (*pb.CompleteVodUploadResponse, error) {
	return c.vod.CompleteVodUpload(ctx, req)
}

// AbortVodUpload cancels an in-progress multipart upload
func (c *GRPCClient) AbortVodUpload(ctx context.Context, tenantID, uploadID string) (*pb.AbortVodUploadResponse, error) {
	return c.vod.AbortVodUpload(ctx, &pb.AbortVodUploadRequest{
		TenantId: tenantID,
		UploadId: uploadID,
	})
}

// GetVodUploadStatus reads server-authoritative state of an in-flight multipart upload.
func (c *GRPCClient) GetVodUploadStatus(ctx context.Context, tenantID, uploadID string) (*pb.GetVodUploadStatusResponse, error) {
	return c.vod.GetVodUploadStatus(ctx, &pb.GetVodUploadStatusRequest{
		TenantId: tenantID,
		UploadId: uploadID,
	})
}

// GetVodAsset gets a single VOD asset by hash
func (c *GRPCClient) GetVodAsset(ctx context.Context, tenantID, artifactHash string) (*pb.VodAssetInfo, error) {
	return c.vod.GetVodAsset(ctx, &pb.GetVodAssetRequest{
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
	})
}

// ListVodAssets lists VOD assets with pagination
func (c *GRPCClient) ListVodAssets(ctx context.Context, tenantID string, pagination *pb.CursorPaginationRequest, streamID *string, opts ...MediaListOptions) (*pb.ListVodAssetsResponse, error) {
	req := &pb.ListVodAssetsRequest{
		TenantId:   tenantID,
		Pagination: pagination,
		StreamId:   streamID,
	}
	applyMediaListOptionsToVod(req, opts...)
	return c.vod.ListVodAssets(ctx, req)
}

func applyMediaListOptionsToClips(req *pb.GetClipsRequest, opts ...MediaListOptions) {
	if len(opts) == 0 {
		return
	}
	opt := opts[0]
	req.Search = opt.Search
	req.SortField = opt.SortField
	req.SortDirection = opt.SortDirection
	req.Offset = opt.Offset
}

func applyMediaListOptionsToDVR(req *pb.ListDVRRecordingsRequest, opts ...MediaListOptions) {
	if len(opts) == 0 {
		return
	}
	opt := opts[0]
	req.Search = opt.Search
	req.SortField = opt.SortField
	req.SortDirection = opt.SortDirection
	req.Offset = opt.Offset
}

func applyMediaListOptionsToVod(req *pb.ListVodAssetsRequest, opts ...MediaListOptions) {
	if len(opts) == 0 {
		return
	}
	opt := opts[0]
	req.Search = opt.Search
	req.SortField = opt.SortField
	req.SortDirection = opt.SortDirection
	req.Offset = opt.Offset
}

// DeleteVodAsset deletes a VOD asset
func (c *GRPCClient) DeleteVodAsset(ctx context.Context, tenantID, artifactHash string) (*pb.DeleteVodAssetResponse, error) {
	return c.vod.DeleteVodAsset(ctx, &pb.DeleteVodAssetRequest{
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
	})
}

// TerminateTenantStreams stops all active streams for a suspended tenant.
// Called by Purser when prepaid balance drops below threshold.
func (c *GRPCClient) TerminateTenantStreams(ctx context.Context, tenantID, reason string) (*pb.TerminateTenantStreamsResponse, error) {
	return c.internal.TerminateTenantStreams(ctx, &pb.TerminateTenantStreamsRequest{
		TenantId: tenantID,
		Reason:   reason,
	})
}

// InvalidateTenantCache clears cached suspension status for a tenant.
// Called by Purser when a tenant is reactivated after payment.
func (c *GRPCClient) InvalidateTenantCache(ctx context.Context, tenantID, reason string) (*pb.InvalidateTenantCacheResponse, error) {
	return c.internal.InvalidateTenantCache(ctx, &pb.InvalidateTenantCacheRequest{
		TenantId: tenantID,
		Reason:   reason,
	})
}

// ============================================================================
// CROSS-SERVICE: BILLING DATA ACCESS
// Called by Purser to avoid cross-service database access.
// ============================================================================

// GetTenantUserCount returns active and total user counts for a tenant.
// Called by Purser billing job for user-based billing calculations.
func (c *GRPCClient) GetTenantUserCount(ctx context.Context, tenantID string) (*pb.GetTenantUserCountResponse, error) {
	return c.internal.GetTenantUserCount(ctx, &pb.GetTenantUserCountRequest{
		TenantId: tenantID,
	})
}

// GetTenantPrimaryUser returns the primary user info for a tenant.
// Called by Purser billing job for billing notifications and invoices.
func (c *GRPCClient) GetTenantPrimaryUser(ctx context.Context, tenantID string) (*pb.GetTenantPrimaryUserResponse, error) {
	return c.internal.GetTenantPrimaryUser(ctx, &pb.GetTenantPrimaryUserRequest{
		TenantId: tenantID,
	})
}

// CreateUserInTenant creates a user in an existing tenant.
// Used by CLI provisioning and admin operations. Requires service token auth.
func (c *GRPCClient) CreateUserInTenant(ctx context.Context, req *pb.CreateUserInTenantRequest) (*pb.CreateUserInTenantResponse, error) {
	return c.internal.CreateUserInTenant(ctx, req)
}

// ============================================================================
// NODE MANAGEMENT (Gateway → Commodore → Foghorn proxy)
// ============================================================================

// SetNodeMode sets a node's operational mode via Foghorn.
func (c *GRPCClient) SetNodeMode(ctx context.Context, req *pb.SetNodeModeRequest) (*pb.SetNodeModeResponse, error) {
	return c.nodeMgmt.SetNodeOperationalMode(ctx, req)
}

// GetNodeHealth returns real-time health for a node via Foghorn.
func (c *GRPCClient) GetNodeHealth(ctx context.Context, req *pb.GetNodeHealthRequest) (*pb.GetNodeHealthResponse, error) {
	return c.nodeMgmt.GetNodeHealth(ctx, req)
}

// ============================================================================
// PUSH TARGET SERVICE (Multistreaming)
// ============================================================================

// GetStreamPushTargets fetches enabled push targets for a stream (internal, used by Foghorn).
func (c *GRPCClient) GetStreamPushTargets(ctx context.Context, streamID, tenantID string) ([]*pb.PushTargetInternal, error) {
	resp, err := c.pushTarget.GetStreamPushTargets(ctx, &pb.GetStreamPushTargetsRequest{
		StreamId: streamID,
		TenantId: tenantID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetPushTargets(), nil
}

// UpdatePushTargetStatus updates the status of a push target (internal, used by Foghorn).
func (c *GRPCClient) UpdatePushTargetStatus(ctx context.Context, id, tenantID, status string, lastError *string) error {
	req := &pb.UpdatePushTargetStatusRequest{
		Id:       id,
		TenantId: tenantID,
		Status:   status,
	}
	if lastError != nil {
		req.LastError = lastError
	}
	_, err := c.pushTarget.UpdatePushTargetStatus(ctx, req)
	return err
}

// CreatePushTarget creates a new push target (Gateway → Commodore).
func (c *GRPCClient) CreatePushTarget(ctx context.Context, req *pb.CreatePushTargetRequest) (*pb.PushTarget, error) {
	return c.pushTarget.CreatePushTarget(ctx, req)
}

// ListPushTargets lists push targets for a stream (Gateway → Commodore).
func (c *GRPCClient) ListPushTargets(ctx context.Context, streamID string) (*pb.ListPushTargetsResponse, error) {
	return c.pushTarget.ListPushTargets(ctx, &pb.ListPushTargetsRequest{StreamId: streamID})
}

// UpdatePushTarget updates a push target (Gateway → Commodore).
func (c *GRPCClient) UpdatePushTarget(ctx context.Context, req *pb.UpdatePushTargetRequest) (*pb.PushTarget, error) {
	return c.pushTarget.UpdatePushTarget(ctx, req)
}

// DeletePushTarget deletes a push target (Gateway → Commodore).
func (c *GRPCClient) DeletePushTarget(ctx context.Context, id string) (*pb.DeletePushTargetResponse, error) {
	return c.pushTarget.DeletePushTarget(ctx, &pb.DeletePushTargetRequest{Id: id})
}

// CreateSigningKey generates a new ES256 keypair. The private PEM in the
// response is shown to the customer ONCE; FrameWorks stores only the public.
func (c *GRPCClient) CreateSigningKey(ctx context.Context, name string) (*pb.CreateSigningKeyResponse, error) {
	return c.playbackAuth.CreateSigningKey(ctx, &pb.CreateSigningKeyRequest{Name: name})
}

// GetSigningKey fetches a single signing key. Tenant-scoped.
func (c *GRPCClient) GetSigningKey(ctx context.Context, id string) (*pb.SigningKey, error) {
	return c.playbackAuth.GetSigningKey(ctx, &pb.GetSigningKeyRequest{Id: id})
}

// ListSigningKeys lists signing keys for the tenant with optional status filter.
func (c *GRPCClient) ListSigningKeys(ctx context.Context, statusFilter string, limit int32, afterID string) (*pb.ListSigningKeysResponse, error) {
	return c.playbackAuth.ListSigningKeys(ctx, &pb.ListSigningKeysRequest{
		StatusFilter: statusFilter,
		Limit:        limit,
		AfterId:      afterID,
	})
}

// RevokeSigningKey marks a signing key revoked. Triggers cache + session
// invalidation across the tenant's protected playback objects.
func (c *GRPCClient) RevokeSigningKey(ctx context.Context, id string) (*pb.SigningKey, error) {
	return c.playbackAuth.RevokeSigningKey(ctx, &pb.RevokeSigningKeyRequest{Id: id})
}

// SetPlaybackPolicy persists a per-object playback access policy and triggers
// the cache-invalidate + invalidate_sessions fanout.
func (c *GRPCClient) SetPlaybackPolicy(ctx context.Context, req *pb.SetPlaybackPolicyRequest) (*pb.SetPlaybackPolicyResponse, error) {
	return c.playbackAuth.SetPlaybackPolicy(ctx, req)
}

// ResolvePlaybackPolicy returns policy data for public reads. Webhook secrets
// are intentionally omitted.
func (c *GRPCClient) ResolvePlaybackPolicy(ctx context.Context, playbackID string) (*pb.ResolvePlaybackPolicyResponse, error) {
	return c.internal.ResolvePlaybackPolicy(ctx, &pb.ResolvePlaybackPolicyRequest{PlaybackId: playbackID})
}

// ResolvePlaybackPolicyForEnforcement returns policy data needed to make an
// allow/deny decision, including the decrypted webhook secret.
func (c *GRPCClient) ResolvePlaybackPolicyForEnforcement(ctx context.Context, playbackID string) (*pb.ResolvePlaybackPolicyResponse, error) {
	return c.internal.ResolvePlaybackPolicy(ctx, &pb.ResolvePlaybackPolicyRequest{
		PlaybackId:           playbackID,
		IncludeWebhookSecret: true,
	})
}

// ResolvePlaybackPolicyByInternalName is the same RPC keyed by MistServer's
// internal stream name — used by Foghorn's USER_NEW handler, which has the
// internal_name from the trigger payload but not the public playback_id.
func (c *GRPCClient) ResolvePlaybackPolicyByInternalName(ctx context.Context, internalName string) (*pb.ResolvePlaybackPolicyResponse, error) {
	return c.internal.ResolvePlaybackPolicy(ctx, &pb.ResolvePlaybackPolicyRequest{
		InternalName:         internalName,
		IncludeWebhookSecret: true,
	})
}

// GetSignedPolicyBundle fetches a freshly minted signed policy bundle for a
// (tenant_id, stream_id) pair. Foghorn caches the returned bundle with the
// soft/hard TTLs encoded in the response; revocation arrives separately via
// playback_policy_invalidation_outbox 'bundle_revoke' entries.
func (c *GRPCClient) GetSignedPolicyBundle(ctx context.Context, tenantID, streamID string) (*pb.GetSignedPolicyBundleResponse, error) {
	return c.internal.GetSignedPolicyBundle(ctx, &pb.GetSignedPolicyBundleRequest{
		TenantId: tenantID,
		StreamId: streamID,
	})
}

// RecordSigningKeyUse records successful JWT use for rotation/audit metadata.
func (c *GRPCClient) RecordSigningKeyUse(ctx context.Context, tenantID, kid string) error {
	_, err := c.internal.RecordSigningKeyUse(ctx, &pb.RecordSigningKeyUseRequest{
		TenantId: tenantID,
		Kid:      kid,
	})
	return err
}
