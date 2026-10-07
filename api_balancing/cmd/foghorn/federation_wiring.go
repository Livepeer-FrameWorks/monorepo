package main

import (
	"context"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/storage"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	foghornpool "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	goredis "github.com/redis/go-redis/v9"
)

// federationPeerDiscovery is the Quartermaster peer census the peer manager
// reads. Production passes the Quartermaster gRPC client, whose WatchPeers the
// peer manager also discovers through this value.
type federationPeerDiscovery interface {
	ListPeers(ctx context.Context, clusterID string) (*quartermasterpb.ListPeersResponse, error)
}

type federationPeersConfig struct {
	ClusterID              string
	ControlCellID          string
	InstanceID             string
	OwnerTenantID          string
	Redis                  goredis.UniversalClient
	Discovery              federationPeerDiscovery
	Pool                   foghornpool.PoolConfig
	Decklog                *decklog.BatchedClient
	Logger                 logging.Logger
	SelfGeo                func() (float64, float64, string)
	CanPurgeMemberships    func(context.Context, []control.AdmissionEffectFence) (map[string]bool, error)
	ArtifactTenantResolver func(ctx context.Context, hashes []string) (map[string]string, error)
}

// federationPeers is this cell's outbound federation: the shared peer cache,
// the peer manager that holds peer identity and tenant scope, and the client
// that calls peer cells. The federation server is built from it, so the
// server's peer-authority checks and peer calls exist from its first request.
type federationPeers struct {
	cache       *federation.RemoteEdgeCache
	peerManager *federation.PeerManager
	client      *federation.FederationClient
	pool        *foghornpool.FoghornPool
}

func newFederationPeers(cfg federationPeersConfig) *federationPeers {
	remoteEdgeCache := federation.NewRemoteEdgeCache(cfg.Redis, cfg.ClusterID, cfg.Logger)
	pool := foghornpool.NewPool(cfg.Pool)
	peerManager := federation.NewPeerManager(federation.PeerManagerConfig{
		ClusterID:              cfg.ClusterID,
		ControlCellID:          cfg.ControlCellID,
		InstanceID:             cfg.InstanceID,
		Pool:                   pool,
		PeerDiscovery:          cfg.Discovery,
		Cache:                  remoteEdgeCache,
		Logger:                 cfg.Logger,
		DecklogClient:          cfg.Decklog,
		OwnerTenantID:          cfg.OwnerTenantID,
		SelfGeoFunc:            cfg.SelfGeo,
		CanPurgeMemberships:    cfg.CanPurgeMemberships,
		ArtifactTenantResolver: cfg.ArtifactTenantResolver,
	})
	return &federationPeers{
		cache:       remoteEdgeCache,
		peerManager: peerManager,
		client:      federation.NewFederationClient(federation.FederationClientConfig{Pool: pool, Logger: cfg.Logger}),
		pool:        pool,
	}
}

// Close stops the peer manager before closing the connection pool its peer
// channels use.
func (p *federationPeers) Close() {
	p.peerManager.Close()
	p.pool.Close()
}

// newFederationServer builds the inbound federation server over this cell's
// peers. A local S3 client is passed only when one exists: a nil
// *storage.S3Client stored in the interface field would read as configured.
func newFederationServer(cfg federation.FederationServerConfig, peers *federationPeers, s3 *storage.S3Client) (*federation.FederationServer, error) {
	cfg.Cache = peers.cache
	cfg.PeerManager = peers.peerManager
	cfg.FedClient = peers.client
	cfg.AllowFederationMutations = true
	if s3 != nil {
		cfg.S3Client = s3
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return federation.NewFederationServer(cfg), nil
}
