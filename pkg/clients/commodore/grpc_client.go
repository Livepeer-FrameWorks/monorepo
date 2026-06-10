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
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	x402pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/x402"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const DefaultServerName = "commodore.internal"

// GRPCClient is the gRPC client for Commodore
type GRPCClient struct {
	conn         *grpc.ClientConn
	internal     commodorepb.InternalServiceClient
	stream       commodorepb.StreamServiceClient
	streamKey    commodorepb.StreamKeyServiceClient
	user         commodorepb.UserServiceClient
	developer    commodorepb.DeveloperServiceClient
	clip         commodorepb.ClipServiceClient
	dvr          commodorepb.DVRServiceClient
	viewer       commodorepb.ViewerServiceClient
	vod          commodorepb.VodServiceClient
	nodeMgmt     commodorepb.NodeManagementServiceClient
	pushTarget   commodorepb.PushTargetServiceClient
	playbackAuth commodorepb.PlaybackAccessControlServiceClient
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
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
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
		internal:     commodorepb.NewInternalServiceClient(conn),
		stream:       commodorepb.NewStreamServiceClient(conn),
		streamKey:    commodorepb.NewStreamKeyServiceClient(conn),
		user:         commodorepb.NewUserServiceClient(conn),
		developer:    commodorepb.NewDeveloperServiceClient(conn),
		clip:         commodorepb.NewClipServiceClient(conn),
		dvr:          commodorepb.NewDVRServiceClient(conn),
		viewer:       commodorepb.NewViewerServiceClient(conn),
		vod:          commodorepb.NewVodServiceClient(conn),
		nodeMgmt:     commodorepb.NewNodeManagementServiceClient(conn),
		pushTarget:   commodorepb.NewPushTargetServiceClient(conn),
		playbackAuth: commodorepb.NewPlaybackAccessControlServiceClient(conn),
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
func (c *GRPCClient) ValidateStreamKey(ctx context.Context, streamKey string, clusterID ...string) (*commodorepb.ValidateStreamKeyResponse, error) {
	cid := ""
	if len(clusterID) > 0 {
		cid = clusterID[0]
	}
	cacheKey := buildValidateStreamKeyCacheKey(streamKey, cid)
	resp, err := c.internal.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
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
			if cachedResp, ok := cached.(*commodorepb.ValidateStreamKeyResponse); ok && cachedResp.GetValid() {
				return cachedResp, nil
			}
		}
	}

	return nil, err
}

// ListManagedStreams returns every mist_native always_on stream eligible to
// run on the requested cluster. Foghorn's managed-stream reconciler calls
// this each tick to build desired state; per-stream admission + cache writes
// then go through ResolveStreamContext below.
func (c *GRPCClient) ListManagedStreams(ctx context.Context, clusterID string) (*commodorepb.ListManagedStreamsResponse, error) {
	return c.internal.ListManagedStreams(ctx, &commodorepb.ListManagedStreamsRequest{ClusterId: clusterID})
}

// RecordStreamActiveCluster pins the cluster currently serving a managed
// stream so commodore.streams.active_ingest_cluster_id reflects the
// elected placement. Called by Foghorn's reconciler after a successful
// ApplyManagedStream; routing for public playback/control consults the
// same column.
func (c *GRPCClient) RecordStreamActiveCluster(ctx context.Context, streamID, clusterID string) (*commodorepb.RecordStreamActiveClusterResponse, error) {
	return c.internal.RecordStreamActiveCluster(ctx, &commodorepb.RecordStreamActiveClusterRequest{
		StreamId:  streamID,
		ClusterId: clusterID,
	})
}

// ClearStreamActiveCluster nulls commodore.streams.active_ingest_cluster_id
// for a managed stream that has been verified retracted from Mist.
// expected_cluster_id is the cluster the caller believes is currently
// recorded; the update is conditional so a stale retract cannot wipe a
// fresher claim from a peer cluster.
func (c *GRPCClient) ClearStreamActiveCluster(ctx context.Context, streamID, expectedClusterID string) (*commodorepb.ClearStreamActiveClusterResponse, error) {
	return c.internal.ClearStreamActiveCluster(ctx, &commodorepb.ClearStreamActiveClusterRequest{
		StreamId:          streamID,
		ExpectedClusterId: expectedClusterID,
	})
}

// ResolveStreamContext returns the admission/materialization fact set for a
// stream identified by stream_id, playback_id, or internal_name. Called by
// Foghorn at per-stream Apply time for ingest modes that bypass
// PUSH_REWRITE (notably mist_native), so the same admission gates and cache
// writes can happen without a stream key.
//
// Pass exactly one identifier; the others must be empty.
func (c *GRPCClient) ResolveStreamContext(ctx context.Context, streamID, playbackID, internalName, clusterID string) (*commodorepb.ResolveStreamContextResponse, error) {
	req := &commodorepb.ResolveStreamContextRequest{
		ClusterId: clusterID,
	}
	switch {
	case streamID != "":
		req.Identifier = &commodorepb.ResolveStreamContextRequest_StreamId{StreamId: streamID}
	case playbackID != "":
		req.Identifier = &commodorepb.ResolveStreamContextRequest_PlaybackId{PlaybackId: playbackID}
	case internalName != "":
		req.Identifier = &commodorepb.ResolveStreamContextRequest_InternalName{InternalName: internalName}
	default:
		return nil, fmt.Errorf("ResolveStreamContext requires exactly one of stream_id / playback_id / internal_name")
	}
	return c.internal.ResolveStreamContext(ctx, req)
}

// ResolvePlaybackID resolves a playback ID to internal stream name
func (c *GRPCClient) ResolvePlaybackID(ctx context.Context, playbackID string) (*commodorepb.ResolvePlaybackIDResponse, error) {
	// Check cache first
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:resolve:" + playbackID
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolvePlaybackID(ctx, &commodorepb.ResolvePlaybackIDRequest{
					PlaybackId: playbackID,
				})
				if err != nil {
					return nil, false, err
				}
				return resp, true, nil
			}); ok {
				return v.(*commodorepb.ResolvePlaybackIDResponse), nil //nolint:errcheck // type guaranteed by cache
			}
		}
	}

	return c.internal.ResolvePlaybackID(ctx, &commodorepb.ResolvePlaybackIDRequest{
		PlaybackId: playbackID,
	})
}

// ResolveInternalName resolves an internal name to tenant context
func (c *GRPCClient) ResolveInternalName(ctx context.Context, internalName string) (*commodorepb.ResolveInternalNameResponse, error) {
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:internal:" + internalName
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolveInternalName(ctx, &commodorepb.ResolveInternalNameRequest{
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
				resp, ok := v.(*commodorepb.ResolveInternalNameResponse)
				if !ok {
					return nil, fmt.Errorf("cached ResolveInternalName response has unexpected type %T", v)
				}
				return resp, nil
			}
		}
	}

	return c.internal.ResolveInternalName(ctx, &commodorepb.ResolveInternalNameRequest{
		InternalName: internalName,
	})
}

// ResolveArtifactPlaybackID resolves an artifact playback ID to artifact identity
func (c *GRPCClient) ResolveArtifactPlaybackID(ctx context.Context, playbackID string) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:artifact:playback:" + playbackID
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolveArtifactPlaybackID(ctx, &commodorepb.ResolveArtifactPlaybackIDRequest{
					PlaybackId: playbackID,
				})
				if err != nil || !resp.Found {
					return nil, false, err
				}
				return resp, true, nil
			}); ok {
				return v.(*commodorepb.ResolveArtifactPlaybackIDResponse), nil //nolint:errcheck // type guaranteed by cache
			}
		}
	}

	return c.internal.ResolveArtifactPlaybackID(ctx, &commodorepb.ResolveArtifactPlaybackIDRequest{
		PlaybackId: playbackID,
	})
}

// ResolveArtifactInternalName resolves an artifact internal routing name to artifact identity
func (c *GRPCClient) ResolveArtifactInternalName(ctx context.Context, internalName string) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:artifact:internal:" + internalName
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolveArtifactInternalName(ctx, &commodorepb.ResolveArtifactInternalNameRequest{
					InternalName: internalName,
				})
				if err != nil || !resp.Found {
					return nil, false, err
				}
				return resp, true, nil
			}); ok {
				return v.(*commodorepb.ResolveArtifactInternalNameResponse), nil //nolint:errcheck // type guaranteed by cache
			}
		}
	}

	return c.internal.ResolveArtifactInternalName(ctx, &commodorepb.ResolveArtifactInternalNameRequest{
		InternalName: internalName,
	})
}

// ResolvePullSourceByInternalName returns the configured upstream pull URI for a
// pull-mode stream. Used by Foghorn STREAM_SOURCE handling and /source origin
// selection. No tenant-scoped caching here — the value is tenant-attributed in
// the response and Foghorn caches per process if needed.
func (c *GRPCClient) ResolvePullSourceByInternalName(ctx context.Context, internalName string) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
	return c.internal.ResolvePullSourceByInternalName(ctx, &commodorepb.ResolvePullSourceByInternalNameRequest{
		InternalName: internalName,
	})
}

// ValidateAPIToken validates a developer API token
func (c *GRPCClient) ValidateAPIToken(ctx context.Context, token string) (*commodorepb.ValidateAPITokenResponse, error) {
	return c.internal.ValidateAPIToken(ctx, &commodorepb.ValidateAPITokenRequest{
		Token: token,
	})
}

// MintMistAdminSession mints a short-TTL session token bound to a single
// edge node. Caller is expected to have authorized the operator at the
// Gateway resolver level — this RPC is the mint primitive only.
func (c *GRPCClient) MintMistAdminSession(ctx context.Context, req *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
	return c.internal.MintMistAdminSession(ctx, req)
}

// ValidateMistAdminSession verifies a session token; expected_node_id MUST
// be set to the connected Helmsman's nodeID by the relay (Foghorn).
func (c *GRPCClient) ValidateMistAdminSession(ctx context.Context, req *commodorepb.ValidateMistAdminSessionRequest) (*commodorepb.ValidateMistAdminSessionResponse, error) {
	return c.internal.ValidateMistAdminSession(ctx, req)
}

// StartDVR initiates DVR recording for a stream (internal, called by Foghorn)
func (c *GRPCClient) StartDVR(ctx context.Context, req *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
	return c.internal.StartDVR(ctx, req)
}

// ============================================================================
// CLIP/DVR REGISTRY (Foghorn → Commodore)
// Business registry for clips and DVR recordings.
// See: docs/architecture/clips-dvr.md
// ============================================================================

// RegisterClip registers a new clip in the business registry
// Called by Foghorn during the CreateClip flow
func (c *GRPCClient) RegisterClip(ctx context.Context, req *commodorepb.RegisterClipRequest) (*commodorepb.RegisterClipResponse, error) {
	return c.internal.RegisterClip(ctx, req)
}

// RegisterDVR registers a new DVR recording in the business registry
// Called by Foghorn during the StartDVR flow
func (c *GRPCClient) RegisterDVR(ctx context.Context, req *commodorepb.RegisterDVRRequest) (*commodorepb.RegisterDVRResponse, error) {
	return c.internal.RegisterDVR(ctx, req)
}

// MarkArtifactThumbnailsReady flips has_thumbnails=TRUE and stamps
// storage_cluster_id on the commodore.{clips, dvr_recordings, vod_assets}
// row matching (tenant_id, asset_key). Idempotent. Called from Foghorn's
// processThumbnailUploaded confirmation site.
func (c *GRPCClient) MarkArtifactThumbnailsReady(ctx context.Context, tenantID string, assetType commodorepb.ArtifactAssetType, assetKey, storageClusterID string) (*commodorepb.MarkArtifactThumbnailsReadyResponse, error) {
	return c.internal.MarkArtifactThumbnailsReady(ctx, &commodorepb.MarkArtifactThumbnailsReadyRequest{
		TenantId:         tenantID,
		AssetType:        assetType,
		AssetKey:         assetKey,
		StorageClusterId: storageClusterID,
	})
}

// UpdateArtifactStorageCluster updates storage_cluster_id only — never
// touches has_thumbnails. Called whenever Foghorn mutates
// foghorn.artifacts.storage_cluster_id.
func (c *GRPCClient) UpdateArtifactStorageCluster(ctx context.Context, tenantID string, assetType commodorepb.ArtifactAssetType, assetKey, storageClusterID string) (*commodorepb.UpdateArtifactStorageClusterResponse, error) {
	return c.internal.UpdateArtifactStorageCluster(ctx, &commodorepb.UpdateArtifactStorageClusterRequest{
		TenantId:         tenantID,
		AssetType:        assetType,
		AssetKey:         assetKey,
		StorageClusterId: storageClusterID,
	})
}

// UpdateArtifactSize projects Foghorn's authoritative artifact byte count into
// the Commodore registry row used for catalog pagination and sorting.
func (c *GRPCClient) UpdateArtifactSize(ctx context.Context, tenantID string, assetType commodorepb.ArtifactAssetType, assetKey string, sizeBytes int64) (*commodorepb.UpdateArtifactSizeResponse, error) {
	return c.internal.UpdateArtifactSize(ctx, &commodorepb.UpdateArtifactSizeRequest{
		TenantId:  tenantID,
		AssetType: assetType,
		AssetKey:  assetKey,
		SizeBytes: sizeBytes,
	})
}

// UpdateClipDuration projects the measured output duration onto the
// commodore clip registry row, so a partial clip (live buffer shallower than
// the requested range) lists with its real length.
func (c *GRPCClient) UpdateClipDuration(ctx context.Context, tenantID, clipHash string, durationMs int64) (*commodorepb.UpdateArtifactSizeResponse, error) {
	return c.internal.UpdateArtifactSize(ctx, &commodorepb.UpdateArtifactSizeRequest{
		TenantId:   tenantID,
		AssetType:  commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP,
		AssetKey:   clipHash,
		DurationMs: &durationMs,
	})
}

// UpdateDVRRetention back-fills retention_until on a finalized DVR.
// Foghorn computes retention_until = ended_at + dvr_retention_days*24h
// from the persisted policy snapshot at FinalizeDVR time and pushes it
// here so commodore.dvr_recordings.retention_until reflects post-end
// retention. Active recordings carry NULL until they finalize.
func (c *GRPCClient) UpdateDVRRetention(ctx context.Context, req *commodorepb.UpdateDVRRetentionRequest) (*commodorepb.UpdateDVRRetentionResponse, error) {
	return c.internal.UpdateDVRRetention(ctx, req)
}

// ============================================================================
// MEDIA RETENTION POLICY (Bridge → Commodore)
// Customer-tunable retention defaults + per-asset overrides.
// ============================================================================

func (c *GRPCClient) GetMediaRetentionPolicy(ctx context.Context, req *commodorepb.GetMediaRetentionPolicyRequest) (*commodorepb.GetMediaRetentionPolicyResponse, error) {
	return c.internal.GetMediaRetentionPolicy(ctx, req)
}

func (c *GRPCClient) SetMediaRetentionPolicy(ctx context.Context, req *commodorepb.SetMediaRetentionPolicyRequest) (*commodorepb.SetMediaRetentionPolicyResponse, error) {
	return c.internal.SetMediaRetentionPolicy(ctx, req)
}

func (c *GRPCClient) UpdateAssetRetention(ctx context.Context, req *commodorepb.UpdateAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error) {
	return c.internal.UpdateAssetRetention(ctx, req)
}

func (c *GRPCClient) ResetAssetRetention(ctx context.Context, req *commodorepb.ResetAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error) {
	return c.internal.ResetAssetRetention(ctx, req)
}

// SetStreamRetentionOverrides writes per-stream DVR/clip retention overrides.
// Commodore clamps positive values down to the tier cap and treats -1 as a
// clear sentinel; the response reports the resolved post-write state.
func (c *GRPCClient) SetStreamRetentionOverrides(ctx context.Context, req *commodorepb.SetStreamRetentionOverridesRequest) (*commodorepb.SetStreamRetentionOverridesResponse, error) {
	return c.internal.SetStreamRetentionOverrides(ctx, req)
}

// TestPlaybackAccess facades Foghorn's dry-run evaluator. Webhook mode can
// take up to ~10s for the customer endpoint to respond — keep timeouts
// generous on the caller side.
func (c *GRPCClient) TestPlaybackAccess(ctx context.Context, req *foghorncontrolpb.TestPlaybackAccessRequest) (*foghorncontrolpb.TestPlaybackAccessResponse, error) {
	return c.internal.TestPlaybackAccess(ctx, req)
}

// RecordPullSourceEvent appends a row to commodore.pull_source_events. Used
// by Foghorn's STREAM_SOURCE handler to audit pull resolution outcomes.
func (c *GRPCClient) RecordPullSourceEvent(ctx context.Context, req *commodorepb.RecordPullSourceEventRequest) error {
	_, err := c.internal.RecordPullSourceEvent(ctx, req)
	return err
}

// ListPullSourceEvents returns the most recent N pull-source resolution
// events for a stream. Tenant-scoped via the JWT.
func (c *GRPCClient) ListPullSourceEvents(ctx context.Context, req *commodorepb.ListPullSourceEventsRequest) (*commodorepb.ListPullSourceEventsResponse, error) {
	return c.internal.ListPullSourceEvents(ctx, req)
}

// ResolveClipHash resolves a clip hash to tenant context
// Used for analytics enrichment and playback authorization
func (c *GRPCClient) ResolveClipHash(ctx context.Context, clipHash string) (*commodorepb.ResolveClipHashResponse, error) {
	// Use cache for context lookups (high frequency during playback/events)
	if c.cache != nil {
		cacheKey := "commodore:clip:" + clipHash
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.internal.ResolveClipHash(ctx, &commodorepb.ResolveClipHashRequest{
				ClipHash: clipHash,
			})
			if err != nil || !resp.Found {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*commodorepb.ResolveClipHashResponse), nil //nolint:errcheck // type guaranteed by cache
		}
	}

	return c.internal.ResolveClipHash(ctx, &commodorepb.ResolveClipHashRequest{
		ClipHash: clipHash,
	})
}

// ResolveDVRHash resolves a DVR hash to tenant context
// Used for analytics enrichment and playback authorization
func (c *GRPCClient) ResolveDVRHash(ctx context.Context, dvrHash string) (*commodorepb.ResolveDVRHashResponse, error) {
	// Use cache for context lookups (high frequency during playback/events)
	if c.cache != nil {
		cacheKey := "commodore:dvr:" + dvrHash
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.internal.ResolveDVRHash(ctx, &commodorepb.ResolveDVRHashRequest{
				DvrHash: dvrHash,
			})
			if err != nil || !resp.Found {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*commodorepb.ResolveDVRHashResponse), nil //nolint:errcheck // type guaranteed by cache
		}
	}

	return c.internal.ResolveDVRHash(ctx, &commodorepb.ResolveDVRHashRequest{
		DvrHash: dvrHash,
	})
}

// ResolveIdentifier provides unified resolution across all Commodore registries
// Checks: streams (internal_name), streams (playback_id), clips, DVR, VOD
// Used by Foghorn for analytics enrichment when local state cache misses
func (c *GRPCClient) ResolveIdentifier(ctx context.Context, identifier string) (*commodorepb.ResolveIdentifierResponse, error) {
	// Use cache for context lookups (high frequency during playback/events)
	if c.cache != nil {
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			cacheKey := tenantID + ":commodore:id:" + identifier
			if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
				resp, err := c.internal.ResolveIdentifier(ctx, &commodorepb.ResolveIdentifierRequest{
					Identifier: identifier,
				})
				if err != nil || !resp.Found {
					return nil, false, err
				}
				return resp, true, nil
			}); ok {
				return v.(*commodorepb.ResolveIdentifierResponse), nil //nolint:errcheck // type guaranteed by cache
			}
		}
	}

	return c.internal.ResolveIdentifier(ctx, &commodorepb.ResolveIdentifierRequest{
		Identifier: identifier,
	})
}

// RegisterVod registers a new VOD asset in the business registry
// Called by Foghorn during CreateVodUpload flow (mirrors DVR/clip pattern)
func (c *GRPCClient) RegisterVod(ctx context.Context, tenantID, userID, filename string, title, description, contentType *string, sizeBytes *int64) (*commodorepb.RegisterVodResponse, error) {
	req := &commodorepb.RegisterVodRequest{
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
func (c *GRPCClient) ResolveVodHash(ctx context.Context, vodHash string) (*commodorepb.ResolveVodHashResponse, error) {
	// Use cache for context lookups (high frequency during playback/events)
	if c.cache != nil {
		cacheKey := "commodore:vod:" + vodHash
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.internal.ResolveVodHash(ctx, &commodorepb.ResolveVodHashRequest{
				VodHash: vodHash,
			})
			if err != nil || !resp.Found {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*commodorepb.ResolveVodHashResponse), nil //nolint:errcheck // type guaranteed by cache
		}
	}

	return c.internal.ResolveVodHash(ctx, &commodorepb.ResolveVodHashRequest{
		VodHash: vodHash,
	})
}

// ResolveVodID resolves a VOD relay ID (vod_assets.id) to VOD hash + tenant context
func (c *GRPCClient) ResolveVodID(ctx context.Context, vodID string) (*commodorepb.ResolveVodIDResponse, error) {
	return c.internal.ResolveVodID(ctx, &commodorepb.ResolveVodIDRequest{
		VodId: vodID,
	})
}

// MintChapterPlaybackID asks Commodore to mint (or return the existing)
// public playback_id for a hidden chapter artifact. Idempotent on
// chapter_id.
func (c *GRPCClient) MintChapterPlaybackID(ctx context.Context, chapterID, tenantID, artifactHash, userID, filename, originClusterID, storageClusterID, streamID string) (*commodorepb.MintChapterPlaybackIDResponse, error) {
	return c.internal.MintChapterPlaybackID(ctx, &commodorepb.MintChapterPlaybackIDRequest{
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

// GetTenantProcessesJSON returns the tenant-level MistServer process config
// JSON for the given legacy stream type ("live" | "vod").
func (c *GRPCClient) GetTenantProcessesJSON(ctx context.Context, tenantID, streamType, clusterID string) (*commodorepb.GetTenantProcessesJSONResponse, error) {
	return c.GetTenantProcessesJSONForStream(ctx, tenantID, streamType, clusterID, "")
}

// GetTenantProcessesJSONForStream returns the stream-aware MistServer process
// config JSON for the given legacy stream type ("live" | "vod").
func (c *GRPCClient) GetTenantProcessesJSONForStream(ctx context.Context, tenantID, streamType, clusterID, streamID string) (*commodorepb.GetTenantProcessesJSONResponse, error) {
	return c.GetTenantProcessesJSONForLifecycle(ctx, tenantID, streamType, clusterID, streamID)
}

// GetTenantProcessesJSONForLifecycle returns the stream-aware MistServer
// process config JSON for a lifecycle ("live", "dvr", "clip",
// "dvr_finalize", or "vod").
func (c *GRPCClient) GetTenantProcessesJSONForLifecycle(ctx context.Context, tenantID, lifecycle, clusterID, streamID string) (*commodorepb.GetTenantProcessesJSONResponse, error) {
	return c.internal.GetTenantProcessesJSON(ctx, &commodorepb.GetTenantProcessesJSONRequest{
		TenantId:   tenantID,
		StreamType: lifecycle,
		ClusterId:  clusterID,
		StreamId:   streamID,
		Lifecycle:  lifecycle,
	})
}

// ResolveChapterPlaybackID maps a public chapter playback_id back to its
// internal identity (chapter_id, tenant_id, artifact_hash). Cached
// because chapter playback lookups happen on every viewer connect.
func (c *GRPCClient) ResolveChapterPlaybackID(ctx context.Context, playbackID string) (*commodorepb.ResolveChapterPlaybackIDResponse, error) {
	if c.cache != nil {
		cacheKey := "commodore:chapter_pb:" + playbackID
		if v, ok, _ := c.cache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) { //nolint:errcheck // miss is recovered by the direct call below
			resp, err := c.internal.ResolveChapterPlaybackID(ctx, &commodorepb.ResolveChapterPlaybackIDRequest{
				PlaybackId: playbackID,
			})
			if err != nil || !resp.GetFound() {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*commodorepb.ResolveChapterPlaybackIDResponse), nil //nolint:errcheck // type guaranteed by cache
		}
	}
	return c.internal.ResolveChapterPlaybackID(ctx, &commodorepb.ResolveChapterPlaybackIDRequest{
		PlaybackId: playbackID,
	})
}

// ============================================================================
// WALLET IDENTITY (x402 / Agent Access)
// ============================================================================

// GetOrCreateWalletUser looks up or creates a tenant/user for a verified wallet address.
// This is called by x402 middleware after verifying the ERC-3009 payment signature.
// If the wallet is unknown, Commodore creates: tenant (prepaid) + user (email=NULL) + wallet_identity.
func (c *GRPCClient) GetOrCreateWalletUser(ctx context.Context, chainType, walletAddress string) (*commodorepb.GetOrCreateWalletUserResponse, error) {
	return c.internal.GetOrCreateWalletUser(ctx, &commodorepb.GetOrCreateWalletUserRequest{
		ChainType:     chainType,
		WalletAddress: walletAddress,
	})
}

// ============================================================================
// STREAM OPERATIONS
// ============================================================================

// CreateStream creates a new stream
func (c *GRPCClient) CreateStream(ctx context.Context, req *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error) {
	return c.stream.CreateStream(ctx, req)
}

// GetStream gets a stream by ID
func (c *GRPCClient) GetStream(ctx context.Context, streamID string) (*commodorepb.Stream, error) {
	return c.stream.GetStream(ctx, &commodorepb.GetStreamRequest{
		StreamId: streamID,
	})
}

// GetStreamsBatch fetches multiple streams by IDs in a single batch call
func (c *GRPCClient) GetStreamsBatch(ctx context.Context, streamIDs []string) (*commodorepb.GetStreamsBatchResponse, error) {
	return c.stream.GetStreamsBatch(ctx, &commodorepb.GetStreamsBatchRequest{StreamIds: streamIDs})
}

// ListStreams lists streams with cursor pagination
func (c *GRPCClient) ListStreams(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
	return c.stream.ListStreams(ctx, &commodorepb.ListStreamsRequest{
		Pagination: pagination,
	})
}

// UpdateStream updates a stream
func (c *GRPCClient) UpdateStream(ctx context.Context, req *commodorepb.UpdateStreamRequest) (*commodorepb.Stream, error) {
	return c.stream.UpdateStream(ctx, req)
}

// DeleteStream deletes a stream
func (c *GRPCClient) DeleteStream(ctx context.Context, streamID string) (*commodorepb.DeleteStreamResponse, error) {
	return c.stream.DeleteStream(ctx, &commodorepb.DeleteStreamRequest{
		StreamId: streamID,
	})
}

// RefreshStreamKey refreshes a stream key
func (c *GRPCClient) RefreshStreamKey(ctx context.Context, streamID string) (*commodorepb.RefreshStreamKeyResponse, error) {
	return c.stream.RefreshStreamKey(ctx, &commodorepb.RefreshStreamKeyRequest{
		StreamId: streamID,
	})
}

// ============================================================================
// STREAM KEY OPERATIONS
// ============================================================================

// CreateStreamKey creates a new stream key
func (c *GRPCClient) CreateStreamKey(ctx context.Context, streamID, keyName string) (*commodorepb.StreamKeyResponse, error) {
	return c.streamKey.CreateStreamKey(ctx, &commodorepb.CreateStreamKeyRequest{
		StreamId: streamID,
		KeyName:  keyName,
	})
}

// ListStreamKeys lists stream keys for a stream
func (c *GRPCClient) ListStreamKeys(ctx context.Context, streamID string, pagination *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamKeysResponse, error) {
	return c.streamKey.ListStreamKeys(ctx, &commodorepb.ListStreamKeysRequest{
		StreamId:   streamID,
		Pagination: pagination,
	})
}

// DeactivateStreamKey deactivates a stream key
func (c *GRPCClient) DeactivateStreamKey(ctx context.Context, streamID, keyID string) error {
	_, err := c.streamKey.DeactivateStreamKey(ctx, &commodorepb.DeactivateStreamKeyRequest{
		StreamId: streamID,
		KeyId:    keyID,
	})
	return err
}

// ============================================================================
// USER OPERATIONS
// ============================================================================

// Login authenticates a user
func (c *GRPCClient) Login(ctx context.Context, req *commodorepb.LoginRequest) (*commodorepb.AuthResponse, error) {
	return c.user.Login(ctx, req)
}

// Register creates a new user
func (c *GRPCClient) Register(ctx context.Context, req *commodorepb.RegisterRequest) (*commodorepb.RegisterResponse, error) {
	return c.user.Register(ctx, req)
}

// GetMe gets the current user profile
func (c *GRPCClient) GetMe(ctx context.Context) (*commodorepb.User, error) {
	return c.user.GetMe(ctx, &commodorepb.GetMeRequest{})
}

// Logout logs out a user (invalidates token)
func (c *GRPCClient) Logout(ctx context.Context, token string) (*commodorepb.LogoutResponse, error) {
	return c.user.Logout(ctx, &commodorepb.LogoutRequest{Token: token})
}

// RefreshToken refreshes an authentication token
func (c *GRPCClient) RefreshToken(ctx context.Context, refreshToken string) (*commodorepb.AuthResponse, error) {
	return c.user.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: refreshToken})
}

// VerifyEmail verifies a user's email with a token
func (c *GRPCClient) VerifyEmail(ctx context.Context, token string) (*commodorepb.VerifyEmailResponse, error) {
	return c.user.VerifyEmail(ctx, &commodorepb.VerifyEmailRequest{Token: token})
}

// ResendVerification resends the email verification link
func (c *GRPCClient) ResendVerification(ctx context.Context, email, turnstileToken string) (*commodorepb.ResendVerificationResponse, error) {
	return c.user.ResendVerification(ctx, &commodorepb.ResendVerificationRequest{
		Email:          email,
		TurnstileToken: turnstileToken,
	})
}

// ForgotPassword initiates password reset flow
func (c *GRPCClient) ForgotPassword(ctx context.Context, email string) (*commodorepb.ForgotPasswordResponse, error) {
	return c.user.ForgotPassword(ctx, &commodorepb.ForgotPasswordRequest{Email: email})
}

// ResetPassword resets a user's password with a token
func (c *GRPCClient) ResetPassword(ctx context.Context, token, password string) (*commodorepb.ResetPasswordResponse, error) {
	return c.user.ResetPassword(ctx, &commodorepb.ResetPasswordRequest{Token: token, Password: password})
}

// UpdateMe updates the current user's profile
func (c *GRPCClient) UpdateMe(ctx context.Context, req *commodorepb.UpdateMeRequest) (*commodorepb.User, error) {
	return c.user.UpdateMe(ctx, req)
}

// UpdateNewsletter updates the user's newsletter subscription
func (c *GRPCClient) UpdateNewsletter(ctx context.Context, subscribed bool) (*commodorepb.UpdateNewsletterResponse, error) {
	return c.user.UpdateNewsletter(ctx, &commodorepb.UpdateNewsletterRequest{Subscribed: subscribed})
}

// GetNewsletterStatus returns the user's current newsletter subscription status
func (c *GRPCClient) GetNewsletterStatus(ctx context.Context) (bool, error) {
	resp, err := c.user.GetNewsletterStatus(ctx, &commodorepb.GetNewsletterStatusRequest{})
	if err != nil {
		return false, err
	}
	return resp.Subscribed, nil
}

// ============================================================================
// WALLET AUTHENTICATION OPERATIONS
// ============================================================================

// WalletLogin authenticates a user via wallet signature, auto-provisioning if new
func (c *GRPCClient) WalletLogin(ctx context.Context, address, message, signature string, attribution *commonpb.SignupAttribution) (*commodorepb.AuthResponse, error) {
	return c.user.WalletLogin(ctx, &commodorepb.WalletLoginRequest{
		WalletAddress: address,
		Message:       message,
		Signature:     signature,
		Attribution:   attribution,
	})
}

// WalletLoginWithX402 authenticates via x402 payload and returns session token + payment info.
func (c *GRPCClient) WalletLoginWithX402(ctx context.Context, payment *x402pb.X402PaymentPayload, clientIP, targetTenantID string, attribution *commonpb.SignupAttribution) (*commodorepb.WalletLoginWithX402Response, error) {
	req := &commodorepb.WalletLoginWithX402Request{
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
func (c *GRPCClient) LinkWallet(ctx context.Context, address, message, signature string) (*commodorepb.WalletIdentity, error) {
	return c.user.LinkWallet(ctx, &commodorepb.LinkWalletRequest{
		WalletAddress: address,
		Message:       message,
		Signature:     signature,
	})
}

// UnlinkWallet removes a wallet from the current user's account
func (c *GRPCClient) UnlinkWallet(ctx context.Context, walletID string) (*commodorepb.UnlinkWalletResponse, error) {
	return c.user.UnlinkWallet(ctx, &commodorepb.UnlinkWalletRequest{
		WalletId: walletID,
	})
}

// ListWallets lists wallets linked to the current user
func (c *GRPCClient) ListWallets(ctx context.Context) (*commodorepb.ListWalletsResponse, error) {
	return c.user.ListWallets(ctx, &commodorepb.ListWalletsRequest{})
}

// LinkEmail adds an email to a wallet-only account (for postpaid upgrade path)
func (c *GRPCClient) LinkEmail(ctx context.Context, email, password string) (*commodorepb.LinkEmailResponse, error) {
	return c.user.LinkEmail(ctx, &commodorepb.LinkEmailRequest{
		Email:    email,
		Password: password,
	})
}

// CompleteAuthorization mints a single-use PKCE authorization code for the
// signed-in user. Called by the gateway on behalf of the webapp /authorize
// page; identity fields come from the gateway session, not the client body.
func (c *GRPCClient) CompleteAuthorization(ctx context.Context, req *commodorepb.CompleteAuthorizationRequest) (*commodorepb.CompleteAuthorizationResponse, error) {
	return c.user.CompleteAuthorization(ctx, req)
}

// ExchangeAuthorizationCode redeems a PKCE authorization code + verifier for
// a session (access + refresh tokens). Called from the native client's
// loopback receiver.
func (c *GRPCClient) ExchangeAuthorizationCode(ctx context.Context, req *commodorepb.ExchangeAuthorizationCodeRequest) (*commodorepb.AuthResponse, error) {
	return c.user.ExchangeAuthorizationCode(ctx, req)
}

// StartDeviceAuthorization initiates a RFC 8628 device-code grant.
func (c *GRPCClient) StartDeviceAuthorization(ctx context.Context, req *commodorepb.StartDeviceAuthorizationRequest) (*commodorepb.StartDeviceAuthorizationResponse, error) {
	return c.user.StartDeviceAuthorization(ctx, req)
}

// PollDeviceAuthorization polls for completion of a device-code grant. While
// pending, the gRPC error carries one of the RFC 8628 §3.5 markers as its
// message: AUTHORIZATION_PENDING, SLOW_DOWN, ACCESS_DENIED, EXPIRED_TOKEN.
func (c *GRPCClient) PollDeviceAuthorization(ctx context.Context, req *commodorepb.PollDeviceAuthorizationRequest) (*commodorepb.AuthResponse, error) {
	return c.user.PollDeviceAuthorization(ctx, req)
}

// LookupDeviceAuthorization returns pending device-code metadata for the
// consent page before the user approves it.
func (c *GRPCClient) LookupDeviceAuthorization(ctx context.Context, req *commodorepb.LookupDeviceAuthorizationRequest) (*commodorepb.LookupDeviceAuthorizationResponse, error) {
	return c.user.LookupDeviceAuthorization(ctx, req)
}

// ApproveDeviceAuthorization stamps the signed-in user's identity onto a
// pending device-code row. Called by the gateway on behalf of the webapp
// /device page after the user confirms the displayed user_code.
func (c *GRPCClient) ApproveDeviceAuthorization(ctx context.Context, req *commodorepb.ApproveDeviceAuthorizationRequest) (*commodorepb.ApproveDeviceAuthorizationResponse, error) {
	return c.user.ApproveDeviceAuthorization(ctx, req)
}

// ============================================================================
// DEVELOPER/API TOKEN OPERATIONS
// ============================================================================

// CreateAPIToken creates a new API token
func (c *GRPCClient) CreateAPIToken(ctx context.Context, req *commodorepb.CreateAPITokenRequest) (*commodorepb.CreateAPITokenResponse, error) {
	return c.developer.CreateAPIToken(ctx, req)
}

// ListAPITokens lists API tokens
func (c *GRPCClient) ListAPITokens(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*commodorepb.ListAPITokensResponse, error) {
	return c.developer.ListAPITokens(ctx, &commodorepb.ListAPITokensRequest{
		Pagination: pagination,
	})
}

// RevokeAPIToken revokes an API token
func (c *GRPCClient) RevokeAPIToken(ctx context.Context, tokenID string) (*commodorepb.RevokeAPITokenResponse, error) {
	return c.developer.RevokeAPIToken(ctx, &commodorepb.RevokeAPITokenRequest{
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
func (c *GRPCClient) CreateClip(ctx context.Context, req *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
	return c.clip.CreateClip(ctx, req)
}

// GetClips lists clips with optional stream_id filter
func (c *GRPCClient) GetClips(ctx context.Context, tenantID string, streamID *string, pagination *commonpb.CursorPaginationRequest, opts ...MediaListOptions) (*sharedpb.GetClipsResponse, error) {
	req := &sharedpb.GetClipsRequest{
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
func (c *GRPCClient) GetClip(ctx context.Context, clipHash string) (*sharedpb.ClipInfo, error) {
	return c.clip.GetClip(ctx, &sharedpb.GetClipRequest{
		ClipHash: clipHash,
	})
}

// DeleteClip deletes a clip
func (c *GRPCClient) DeleteClip(ctx context.Context, clipHash string) error {
	_, err := c.clip.DeleteClip(ctx, &sharedpb.DeleteClipRequest{
		ClipHash: clipHash,
	})
	return err
}

// ============================================================================
// DVR OPERATIONS (Gateway → Commodore → Foghorn proxy)
// ============================================================================

// StopDVR stops a DVR recording
func (c *GRPCClient) StopDVR(ctx context.Context, dvrHash string) error {
	_, err := c.dvr.StopDVR(ctx, &sharedpb.StopDVRRequest{
		DvrHash: dvrHash,
	})
	return err
}

// DeleteDVR deletes a DVR recording
func (c *GRPCClient) DeleteDVR(ctx context.Context, dvrHash string) error {
	_, err := c.dvr.DeleteDVR(ctx, &sharedpb.DeleteDVRRequest{
		DvrHash: dvrHash,
	})
	return err
}

// RetrieveDVRChapter returns chapter metadata (state, range, public
// playback_id) for a single chapter. Customer-facing path:
// api_gateway → Commodore → Foghorn.
func (c *GRPCClient) RetrieveDVRChapter(ctx context.Context, req *foghorncontrolpb.RetrieveDVRChapterRequest) (*foghorncontrolpb.RetrieveDVRChapterResponse, error) {
	return c.internal.RetrieveDVRChapter(ctx, req)
}

// ListDVRChapters paginates chapter rows for player navigation.
func (c *GRPCClient) ListDVRChapters(ctx context.Context, req *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, error) {
	return c.internal.ListDVRChapters(ctx, req)
}

// ListStorageArtifacts returns the unified account storage browser projection.
func (c *GRPCClient) ListStorageArtifacts(ctx context.Context, req *commodorepb.ListStorageArtifactsRequest) (*commodorepb.ListStorageArtifactsResponse, error) {
	return c.internal.ListStorageArtifacts(ctx, req)
}

// ListDVRRequests lists DVR recordings with filters
func (c *GRPCClient) ListDVRRequests(ctx context.Context, tenantID string, streamID *string, pagination *commonpb.CursorPaginationRequest, opts ...MediaListOptions) (*sharedpb.ListDVRRecordingsResponse, error) {
	req := &sharedpb.ListDVRRecordingsRequest{
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
func (c *GRPCClient) ResolveViewerEndpoint(ctx context.Context, contentID, viewerIP, viewerToken string) (*sharedpb.ViewerEndpointResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("CRITICAL: Commodore GRPCClient is nil")
	}
	if c.viewer == nil {
		return nil, fmt.Errorf("CRITICAL: Commodore.viewer client is nil - gRPC connection failed or not initialized?")
	}
	req := &sharedpb.ViewerEndpointRequest{
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
func (c *GRPCClient) ResolveIngestEndpoint(ctx context.Context, streamKey, viewerIP string) (*sharedpb.IngestEndpointResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("CRITICAL: Commodore GRPCClient is nil")
	}
	if c.viewer == nil {
		return nil, fmt.Errorf("CRITICAL: Commodore.viewer client is nil - gRPC connection failed or not initialized?")
	}
	req := &sharedpb.IngestEndpointRequest{
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
func (c *GRPCClient) CreateVodUpload(ctx context.Context, req *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, error) {
	return c.vod.CreateVodUpload(ctx, req)
}

// CompleteVodUpload finalizes a multipart upload after all parts are uploaded
func (c *GRPCClient) CompleteVodUpload(ctx context.Context, req *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, error) {
	return c.vod.CompleteVodUpload(ctx, req)
}

// AbortVodUpload cancels an in-progress multipart upload
func (c *GRPCClient) AbortVodUpload(ctx context.Context, tenantID, uploadID string) (*sharedpb.AbortVodUploadResponse, error) {
	return c.vod.AbortVodUpload(ctx, &sharedpb.AbortVodUploadRequest{
		TenantId: tenantID,
		UploadId: uploadID,
	})
}

// GetVodUploadStatus reads server-authoritative state of an in-flight multipart upload.
func (c *GRPCClient) GetVodUploadStatus(ctx context.Context, tenantID, uploadID string) (*sharedpb.GetVodUploadStatusResponse, error) {
	return c.vod.GetVodUploadStatus(ctx, &sharedpb.GetVodUploadStatusRequest{
		TenantId: tenantID,
		UploadId: uploadID,
	})
}

// GetVodAsset gets a single VOD asset by hash
func (c *GRPCClient) GetVodAsset(ctx context.Context, tenantID, artifactHash string) (*sharedpb.VodAssetInfo, error) {
	return c.vod.GetVodAsset(ctx, &sharedpb.GetVodAssetRequest{
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
	})
}

// ListVodAssets lists VOD assets with pagination
func (c *GRPCClient) ListVodAssets(ctx context.Context, tenantID string, pagination *commonpb.CursorPaginationRequest, streamID *string, opts ...MediaListOptions) (*sharedpb.ListVodAssetsResponse, error) {
	req := &sharedpb.ListVodAssetsRequest{
		TenantId:   tenantID,
		Pagination: pagination,
		StreamId:   streamID,
	}
	applyMediaListOptionsToVod(req, opts...)
	return c.vod.ListVodAssets(ctx, req)
}

func applyMediaListOptionsToClips(req *sharedpb.GetClipsRequest, opts ...MediaListOptions) {
	if len(opts) == 0 {
		return
	}
	opt := opts[0]
	req.Search = opt.Search
	req.SortField = opt.SortField
	req.SortDirection = opt.SortDirection
	req.Offset = opt.Offset
}

func applyMediaListOptionsToDVR(req *sharedpb.ListDVRRecordingsRequest, opts ...MediaListOptions) {
	if len(opts) == 0 {
		return
	}
	opt := opts[0]
	req.Search = opt.Search
	req.SortField = opt.SortField
	req.SortDirection = opt.SortDirection
	req.Offset = opt.Offset
}

func applyMediaListOptionsToVod(req *sharedpb.ListVodAssetsRequest, opts ...MediaListOptions) {
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
func (c *GRPCClient) DeleteVodAsset(ctx context.Context, tenantID, artifactHash string) (*sharedpb.DeleteVodAssetResponse, error) {
	return c.vod.DeleteVodAsset(ctx, &sharedpb.DeleteVodAssetRequest{
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
	})
}

// TerminateTenantStreams stops all active streams for a suspended tenant.
// Called by Purser when prepaid balance drops below threshold.
func (c *GRPCClient) TerminateTenantStreams(ctx context.Context, tenantID, reason string) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
	return c.internal.TerminateTenantStreams(ctx, &foghorncontrolpb.TerminateTenantStreamsRequest{
		TenantId: tenantID,
		Reason:   reason,
	})
}

// InvalidateTenantCache clears cached suspension status for a tenant.
// Called by Purser when a tenant is reactivated after payment.
func (c *GRPCClient) InvalidateTenantCache(ctx context.Context, tenantID, reason string) (*foghorncontrolpb.InvalidateTenantCacheResponse, error) {
	return c.internal.InvalidateTenantCache(ctx, &foghorncontrolpb.InvalidateTenantCacheRequest{
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
func (c *GRPCClient) GetTenantUserCount(ctx context.Context, tenantID string) (*commodorepb.GetTenantUserCountResponse, error) {
	return c.internal.GetTenantUserCount(ctx, &commodorepb.GetTenantUserCountRequest{
		TenantId: tenantID,
	})
}

// GetTenantPrimaryUser returns the primary user info for a tenant.
// Called by Purser billing job for billing notifications and invoices.
func (c *GRPCClient) GetTenantPrimaryUser(ctx context.Context, tenantID string) (*commodorepb.GetTenantPrimaryUserResponse, error) {
	return c.internal.GetTenantPrimaryUser(ctx, &commodorepb.GetTenantPrimaryUserRequest{
		TenantId: tenantID,
	})
}

// CreateUserInTenant creates a user in an existing tenant.
// Used by CLI provisioning and admin operations. Requires service token auth.
func (c *GRPCClient) CreateUserInTenant(ctx context.Context, req *commodorepb.CreateUserInTenantRequest) (*commodorepb.CreateUserInTenantResponse, error) {
	return c.internal.CreateUserInTenant(ctx, req)
}

// ============================================================================
// NODE MANAGEMENT (Gateway → Commodore → Foghorn proxy)
// ============================================================================

// SetNodeMode sets a node's operational mode via Foghorn.
func (c *GRPCClient) SetNodeMode(ctx context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error) {
	return c.nodeMgmt.SetNodeOperationalMode(ctx, req)
}

// GetNodeHealth returns real-time health for a node via Foghorn.
func (c *GRPCClient) GetNodeHealth(ctx context.Context, req *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, error) {
	return c.nodeMgmt.GetNodeHealth(ctx, req)
}

// ============================================================================
// PUSH TARGET SERVICE (Multistreaming)
// ============================================================================

// GetStreamPushTargets fetches enabled push targets for a stream (internal, used by Foghorn).
func (c *GRPCClient) GetStreamPushTargets(ctx context.Context, streamID, tenantID string) ([]*commodorepb.PushTargetInternal, error) {
	resp, err := c.pushTarget.GetStreamPushTargets(ctx, &commodorepb.GetStreamPushTargetsRequest{
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
	req := &commodorepb.UpdatePushTargetStatusRequest{
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
func (c *GRPCClient) CreatePushTarget(ctx context.Context, req *commodorepb.CreatePushTargetRequest) (*commodorepb.PushTarget, error) {
	return c.pushTarget.CreatePushTarget(ctx, req)
}

// ListPushTargets lists push targets for a stream (Gateway → Commodore).
func (c *GRPCClient) ListPushTargets(ctx context.Context, streamID string) (*commodorepb.ListPushTargetsResponse, error) {
	return c.pushTarget.ListPushTargets(ctx, &commodorepb.ListPushTargetsRequest{StreamId: streamID})
}

// UpdatePushTarget updates a push target (Gateway → Commodore).
func (c *GRPCClient) UpdatePushTarget(ctx context.Context, req *commodorepb.UpdatePushTargetRequest) (*commodorepb.PushTarget, error) {
	return c.pushTarget.UpdatePushTarget(ctx, req)
}

// DeletePushTarget deletes a push target (Gateway → Commodore).
func (c *GRPCClient) DeletePushTarget(ctx context.Context, id string) (*commodorepb.DeletePushTargetResponse, error) {
	return c.pushTarget.DeletePushTarget(ctx, &commodorepb.DeletePushTargetRequest{Id: id})
}

// CreateSigningKey generates a new ES256 keypair. The private PEM in the
// response is shown to the customer ONCE; FrameWorks stores only the public.
func (c *GRPCClient) CreateSigningKey(ctx context.Context, name string) (*commodorepb.CreateSigningKeyResponse, error) {
	return c.playbackAuth.CreateSigningKey(ctx, &commodorepb.CreateSigningKeyRequest{Name: name})
}

// GetSigningKey fetches a single signing key. Tenant-scoped.
func (c *GRPCClient) GetSigningKey(ctx context.Context, id string) (*commodorepb.SigningKey, error) {
	return c.playbackAuth.GetSigningKey(ctx, &commodorepb.GetSigningKeyRequest{Id: id})
}

// ListSigningKeys lists signing keys for the tenant with optional status filter.
func (c *GRPCClient) ListSigningKeys(ctx context.Context, statusFilter string, limit int32, afterID string) (*commodorepb.ListSigningKeysResponse, error) {
	return c.playbackAuth.ListSigningKeys(ctx, &commodorepb.ListSigningKeysRequest{
		StatusFilter: statusFilter,
		Limit:        limit,
		AfterId:      afterID,
	})
}

// RevokeSigningKey marks a signing key revoked. Triggers cache + session
// invalidation across the tenant's protected playback objects.
func (c *GRPCClient) RevokeSigningKey(ctx context.Context, id string) (*commodorepb.SigningKey, error) {
	return c.playbackAuth.RevokeSigningKey(ctx, &commodorepb.RevokeSigningKeyRequest{Id: id})
}

// SetPlaybackPolicy persists a per-object playback access policy and triggers
// the cache-invalidate + invalidate_sessions fanout.
func (c *GRPCClient) SetPlaybackPolicy(ctx context.Context, req *commodorepb.SetPlaybackPolicyRequest) (*commodorepb.SetPlaybackPolicyResponse, error) {
	return c.playbackAuth.SetPlaybackPolicy(ctx, req)
}

// ResolvePlaybackPolicy returns policy data for public reads. Webhook secrets
// are intentionally omitted.
func (c *GRPCClient) ResolvePlaybackPolicy(ctx context.Context, playbackID string) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
	return c.internal.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: playbackID})
}

// ResolvePlaybackPolicyForEnforcement returns policy data needed to make an
// allow/deny decision, including the decrypted webhook secret.
func (c *GRPCClient) ResolvePlaybackPolicyForEnforcement(ctx context.Context, playbackID string) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
	return c.internal.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{
		PlaybackId:           playbackID,
		IncludeWebhookSecret: true,
	})
}

// ResolvePlaybackPolicyByInternalName is the same RPC keyed by MistServer's
// internal stream name — used by Foghorn's USER_NEW handler, which has the
// internal_name from the trigger payload but not the public playback_id.
func (c *GRPCClient) ResolvePlaybackPolicyByInternalName(ctx context.Context, internalName string) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
	return c.internal.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{
		InternalName:         internalName,
		IncludeWebhookSecret: true,
	})
}

// GetSignedPolicyBundle fetches a freshly minted signed policy bundle for a
// (tenant_id, stream_id) pair. Foghorn caches the returned bundle with the
// soft/hard TTLs encoded in the response; revocation arrives separately via
// playback_policy_invalidation_outbox 'bundle_revoke' entries.
func (c *GRPCClient) GetSignedPolicyBundle(ctx context.Context, tenantID, streamID string) (*commodorepb.GetSignedPolicyBundleResponse, error) {
	return c.internal.GetSignedPolicyBundle(ctx, &commodorepb.GetSignedPolicyBundleRequest{
		TenantId: tenantID,
		StreamId: streamID,
	})
}

// RecordSigningKeyUse records successful JWT use for rotation/audit metadata.
func (c *GRPCClient) RecordSigningKeyUse(ctx context.Context, tenantID, kid string) error {
	_, err := c.internal.RecordSigningKeyUse(ctx, &commodorepb.RecordSigningKeyUseRequest{
		TenantId: tenantID,
		Kid:      kid,
	})
	return err
}
