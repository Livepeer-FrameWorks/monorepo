package federation

import (
	"context"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	foghornfed "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn/federation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// federationContext strips user JWT from the context so the client interceptor
// falls through to the service token for service-to-service federation RPCs.
func federationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxkeys.KeyJWTToken, "")
}

// FederationClient wraps FoghornPool to provide federation-specific calls.
// Each method fetches (or lazily creates) the GRPCClient for the target
// cluster, then invokes the corresponding FoghornFederation RPC.
type FederationClient struct {
	pool    *foghorn.FoghornPool
	logger  logging.Logger
	timeout time.Duration
}

// FederationClientConfig holds dependencies for the federation client.
type FederationClientConfig struct {
	Pool    *foghorn.FoghornPool
	Logger  logging.Logger
	Timeout time.Duration // per-call timeout (default 10s)
}

// NewFederationClient creates a new federation client backed by the pool.
func NewFederationClient(cfg FederationClientConfig) *FederationClient {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &FederationClient{
		pool:    cfg.Pool,
		logger:  cfg.Logger,
		timeout: cfg.Timeout,
	}
}

// QueryStream asks a peer cluster whether it has a stream and returns scored candidates.
func (c *FederationClient) QueryStream(ctx context.Context, clusterID, addr string, req *foghornfederationpb.QueryStreamRequest) (*foghornfederationpb.QueryStreamResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return foghornfed.For(client).Federation().QueryStream(ctx, req)
}

// NotifyOriginPull tells the origin cluster that we intend to pull a stream.
func (c *FederationClient) NotifyOriginPull(ctx context.Context, clusterID, addr string, req *foghornfederationpb.OriginPullNotification) (*foghornfederationpb.OriginPullAck, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return foghornfed.For(client).Federation().NotifyOriginPull(ctx, req)
}

// PrepareArtifact requests a cross-cluster artifact be made available.
func (c *FederationClient) PrepareArtifact(ctx context.Context, clusterID, addr string, req *foghornfederationpb.PrepareArtifactRequest) (*foghornfederationpb.PrepareArtifactResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return foghornfed.For(client).Federation().PrepareArtifact(ctx, req)
}

// MintStorageURLs asks the storage-cluster Foghorn pool to issue presigned
// PUT URLs against its S3 backing for an upload that this cluster cannot
// mint locally.
func (c *FederationClient) MintStorageURLs(ctx context.Context, clusterID, addr string, req *foghornfederationpb.MintStorageURLsRequest) (*foghornfederationpb.MintStorageURLsResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return foghornfed.For(client).Federation().MintStorageURLs(ctx, req)
}

// DeleteStorageObjects asks the storage-cluster Foghorn pool to delete an
// artifact's S3 bytes from its local backing. The caller resolves the
// target (s3_key/s3_prefix) from its authoritative row and passes it on
// the wire; the callee validates ownership/tenant/shape and operates on
// exactly the supplied target.
func (c *FederationClient) DeleteStorageObjects(ctx context.Context, clusterID, addr string, req *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return foghornfed.For(client).Federation().DeleteStorageObjects(ctx, req)
}

// CreateRemoteClip requests the origin cluster to create a clip on behalf of this cluster.
func (c *FederationClient) CreateRemoteClip(ctx context.Context, clusterID, addr string, req *foghornfederationpb.RemoteClipRequest) (*foghornfederationpb.RemoteClipResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return foghornfed.For(client).Federation().CreateRemoteClip(ctx, req)
}

// CreateRemoteDVR requests the origin cluster to start a DVR recording on behalf of this cluster.
func (c *FederationClient) CreateRemoteDVR(ctx context.Context, clusterID, addr string, req *foghornfederationpb.RemoteDVRRequest) (*foghornfederationpb.RemoteDVRResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return foghornfed.For(client).Federation().CreateRemoteDVR(ctx, req)
}

// ListTenantArtifacts asks a peer cluster for all artifact metadata belonging to a tenant.
func (c *FederationClient) ListTenantArtifacts(ctx context.Context, clusterID, addr string, req *foghornfederationpb.ListTenantArtifactsRequest) (*foghornfederationpb.ListTenantArtifactsResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), 30*time.Second) // longer timeout for bulk listing
	defer cancel()

	return foghornfed.For(client).Federation().ListTenantArtifacts(ctx, req)
}

// ForwardArtifactCommand forwards an artifact lifecycle command to a peer cluster.
func (c *FederationClient) ForwardArtifactCommand(ctx context.Context, clusterID, addr string, req *foghornfederationpb.ForwardArtifactCommandRequest) (*foghornfederationpb.ForwardArtifactCommandResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	fedCtx := federationContext(ctx)
	if _, hasDeadline := fedCtx.Deadline(); hasDeadline {
		return foghornfed.For(client).Federation().ForwardArtifactCommand(fedCtx, req)
	}

	ctx, cancel := context.WithTimeout(fedCtx, c.timeout)
	defer cancel()

	return foghornfed.For(client).Federation().ForwardArtifactCommand(ctx, req)
}

// OpenPeerChannel opens a bidirectional PeerChannel stream to a peer cluster.
// The caller is responsible for sending/receiving PeerMessages on the returned stream.
func (c *FederationClient) OpenPeerChannel(ctx context.Context, clusterID, addr string) (foghornfederationpb.FoghornFederation_PeerChannelClient, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	return foghornfed.For(client).Federation().PeerChannel(federationContext(ctx))
}
