package federation

import (
	"context"
	"time"

	"frameworks/pkg/clients/foghorn"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
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
func (c *FederationClient) QueryStream(ctx context.Context, clusterID, addr string, req *pb.QueryStreamRequest) (*pb.QueryStreamResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return client.Federation().QueryStream(ctx, req)
}

// NotifyOriginPull tells the origin cluster that we intend to pull a stream.
func (c *FederationClient) NotifyOriginPull(ctx context.Context, clusterID, addr string, req *pb.OriginPullNotification) (*pb.OriginPullAck, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return client.Federation().NotifyOriginPull(ctx, req)
}

// PrepareArtifact requests a cross-cluster artifact be made available.
func (c *FederationClient) PrepareArtifact(ctx context.Context, clusterID, addr string, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return client.Federation().PrepareArtifact(ctx, req)
}

// CreateRemoteClip requests the origin cluster to create a clip on behalf of this cluster.
func (c *FederationClient) CreateRemoteClip(ctx context.Context, clusterID, addr string, req *pb.RemoteClipRequest) (*pb.RemoteClipResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return client.Federation().CreateRemoteClip(ctx, req)
}

// CreateRemoteDVR requests the origin cluster to start a DVR recording on behalf of this cluster.
func (c *FederationClient) CreateRemoteDVR(ctx context.Context, clusterID, addr string, req *pb.RemoteDVRRequest) (*pb.RemoteDVRResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return client.Federation().CreateRemoteDVR(ctx, req)
}

// ListTenantArtifacts asks a peer cluster for all artifact metadata belonging to a tenant.
func (c *FederationClient) ListTenantArtifacts(ctx context.Context, clusterID, addr string, req *pb.ListTenantArtifactsRequest) (*pb.ListTenantArtifactsResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), 30*time.Second) // longer timeout for bulk listing
	defer cancel()

	return client.Federation().ListTenantArtifacts(ctx, req)
}

// ForwardArtifactCommand forwards an artifact lifecycle command to a peer cluster.
func (c *FederationClient) ForwardArtifactCommand(ctx context.Context, clusterID, addr string, req *pb.ForwardArtifactCommandRequest) (*pb.ForwardArtifactCommandResponse, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(federationContext(ctx), c.timeout)
	defer cancel()

	return client.Federation().ForwardArtifactCommand(ctx, req)
}

// OpenPeerChannel opens a bidirectional PeerChannel stream to a peer cluster.
// The caller is responsible for sending/receiving PeerMessages on the returned stream.
func (c *FederationClient) OpenPeerChannel(ctx context.Context, clusterID, addr string) (pb.FoghornFederation_PeerChannelClient, error) {
	client, err := c.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}

	return client.Federation().PeerChannel(federationContext(ctx))
}
