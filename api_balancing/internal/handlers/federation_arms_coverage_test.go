package handlers

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"

	commodorecli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

// =============================================================================
// FedArms harness — unlocks the cross-cluster /source federation arms that
// earlier waves flagged unreachable: handleGetPullSource's eligible==0 ->
// pull_federated, and handleGetSource's lb-miss -> remote_source. Both bottom
// out in arrangeRemoteOriginPullFromSource -> resolveRemoteSourceCandidate ->
// federationClient.QueryStream + ArrangeOriginPull (NotifyOriginPull). The full
// stack is faked end-to-end so the routing DECISION (which DTSC URL, which
// remote cluster, that nothing leaks when the origin peer is revoked) is the
// thing under test, not the gRPC plumbing.
// =============================================================================

const (
	fedArmsLocalCluster  = "cluster-local-fedarms"
	fedArmsOriginCluster = "cluster-origin-fedarms"
)

// fedArmsFederationServer is a loopback FoghornFederation service. QueryStream
// returns the origin cluster's edge candidate; NotifyOriginPull accepts and
// hands back the DTSC URL the puller should dial. These are the only two RPCs
// the /source federation arms invoke.
type fedArmsFederationServer struct {
	foghornfederationpb.UnimplementedFoghornFederationServer

	queryResp  *foghornfederationpb.QueryStreamResponse
	originResp *foghornfederationpb.OriginPullAck

	gotQueryStream    string
	gotNotifyDestNode string
}

func (s *fedArmsFederationServer) QueryStream(_ context.Context, req *foghornfederationpb.QueryStreamRequest) (*foghornfederationpb.QueryStreamResponse, error) {
	s.gotQueryStream = req.GetStreamName()
	return s.queryResp, nil
}

func (s *fedArmsFederationServer) NotifyOriginPull(_ context.Context, req *foghornfederationpb.OriginPullNotification) (*foghornfederationpb.OriginPullAck, error) {
	s.gotNotifyDestNode = req.GetDestNodeId()
	return s.originResp, nil
}

// fixedPeerResolverFedArms satisfies the handlers peerAddrResolver interface
// (the seam *federation.PeerManager normally fills). It maps every cluster to
// one loopback addr so GetPeerAddr is non-empty and federation proceeds.
type fixedPeerResolverFedArms struct {
	addr string
}

func (f fixedPeerResolverFedArms) GetPeerAddr(_ string) string            { return f.addr }
func (f fixedPeerResolverFedArms) GetPeerGeo(_ string) (float64, float64) { return 0, 0 }

// fedArmsStack is the assembled fake cross-cluster stack with its restore hook.
type fedArmsStack struct {
	server *fedArmsFederationServer
}

// setupFedArmsStack wires the complete federation arm dependency graph and
// restores every global on cleanup:
//   - a real loopback FoghornFederation gRPC server (server)
//   - a real *foghorn.FoghornPool dialing it insecurely
//   - a real *federation.FederationClient over that pool (handlers global)
//   - a fixed peerManager pointing every cluster at the loopback addr
//   - a miniredis-backed RemoteEdgeCache (origin-pull lock store)
//   - a real control StreamRegistry keyed on the LOCAL cluster (MarkReplicating
//     target) and the matching control localClusterID
//   - originPullInstanceID so the lock owner is non-empty
//
// commodoreClient (the origin/peer-envelope source) is wired separately by the
// caller via startBalancingCommodoreFake so each test controls the resolution.
func setupFedArmsStack(t *testing.T, queryResp *foghornfederationpb.QueryStreamResponse, originResp *foghornfederationpb.OriginPullAck) *fedArmsStack {
	t.Helper()

	srv := &fedArmsFederationServer{queryResp: queryResp, originResp: originResp}
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	foghornfederationpb.RegisterFoghornFederationServer(grpcSrv, srv)
	go func() { _ = grpcSrv.Serve(lis) }()

	pool := foghorn.NewPool(foghorn.PoolConfig{
		Logger:        logging.NewLogger(),
		AllowInsecure: true,
		Timeout:       5 * time.Second,
	})
	fedClient := federation.NewFederationClient(federation.FederationClientConfig{
		Pool:    pool,
		Logger:  logging.NewLogger(),
		Timeout: 5 * time.Second,
	})

	mr := miniredis.RunT(t)
	redisCli := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cache := federation.NewRemoteEdgeCache(redisCli, fedArmsLocalCluster, logging.NewLogger())

	// Local cluster identity: the registry MUST be keyed on the same cluster as
	// control.GetLocalClusterID()/handlers.clusterID, because MarkReplicating
	// writes the Location under r.clusterID and LocalReplication reads it back
	// from that key. Origin cluster must differ so the cross-cluster path fires.
	prevLocalCluster := control.GetLocalClusterID()
	control.SetLocalClusterID(fedArmsLocalCluster)
	prevRegistry := control.StreamRegistryInstance
	control.StreamRegistryInstance = control.NewStreamRegistry(nil, fedArmsLocalCluster, time.Minute)

	prevClusterID := clusterID
	clusterID = fedArmsLocalCluster

	prevFed := federationClient
	prevPeer := peerManager
	prevCache := remoteEdgeCache
	prevInstance := originPullInstanceID
	federationClient = fedClient
	peerManager = fixedPeerResolverFedArms{addr: lis.Addr().String()}
	remoteEdgeCache = cache
	originPullInstanceID = "foghorn-fedarms"

	t.Cleanup(func() {
		federationClient = prevFed
		peerManager = prevPeer
		remoteEdgeCache = prevCache
		originPullInstanceID = prevInstance
		clusterID = prevClusterID
		control.StreamRegistryInstance = prevRegistry
		control.SetLocalClusterID(prevLocalCluster)
		_ = pool.Close()
		_ = redisCli.Close()
		grpcSrv.Stop()
		_ = lis.Close()
	})

	return &fedArmsStack{server: srv}
}

// fedArmsCommodoreFake is a self-contained Commodore InternalService double for
// the federation arms. Unlike commodoreBalancingFake it also answers
// ResolvePullSourceByInternalName (the pull+ federation arm needs both the
// pull-source row and the origin/peer envelope). Each RPC is a settable func.
type fedArmsCommodoreFake struct {
	commodorepb.UnimplementedInternalServiceServer

	internalName func(context.Context, *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error)
	pullSource   func(context.Context, *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error)
}

func (f *fedArmsCommodoreFake) ResolveInternalName(ctx context.Context, req *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
	if f.internalName != nil {
		return f.internalName(ctx, req)
	}
	return &commodorepb.ResolveInternalNameResponse{}, nil
}

func (f *fedArmsCommodoreFake) ResolvePullSourceByInternalName(ctx context.Context, req *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
	if f.pullSource != nil {
		return f.pullSource(ctx, req)
	}
	return &commodorepb.ResolvePullSourceByInternalNameResponse{}, nil
}

// startFedArmsCommodore serves the fake on a loopback gRPC listener, builds a
// real *commodore.GRPCClient, and points BOTH the resolution-path global
// (control.CommodoreClient) and the handlers-package global (commodoreClient)
// at it. Everything is restored on cleanup.
func startFedArmsCommodore(t *testing.T, fake *fedArmsCommodoreFake) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := commodorecli.NewGRPCClient(commodorecli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("commodore client: %v", err)
	}

	prevControl := control.CommodoreClient
	prevHandlers := commodoreClient
	control.CommodoreClient = client
	commodoreClient = client
	t.Cleanup(func() {
		control.CommodoreClient = prevControl
		commodoreClient = prevHandlers
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

// fedArmsCommodore wires a Commodore that resolves the stream's origin cluster
// to originCluster and returns a cluster-peer envelope. The envelope is the
// front-door reauthorization gate (AuthoritativeClusterServable): without the
// origin cluster present, federation must refuse.
func fedArmsCommodore(t *testing.T, tenantID, originCluster string, peers []*clusterpeerpb.TenantClusterPeer) {
	t.Helper()
	startFedArmsCommodore(t, &fedArmsCommodoreFake{
		internalName: func(_ context.Context, _ *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			return &commodorepb.ResolveInternalNameResponse{
				InternalName:    "show",
				TenantId:        tenantID,
				OriginClusterId: originCluster,
				ClusterPeers:    peers,
			}, nil
		},
	})
}

// peerEnvelope builds a one-entry cluster-peer envelope listing clusterID.
func peerEnvelopeFedArms(clusterIDs ...string) []*clusterpeerpb.TenantClusterPeer {
	peers := make([]*clusterpeerpb.TenantClusterPeer, 0, len(clusterIDs))
	for _, id := range clusterIDs {
		peers = append(peers, &clusterpeerpb.TenantClusterPeer{ClusterId: id})
	}
	return peers
}

// Invariant: handleGetSource on a live+ stream that no LOCAL node carries falls
// through to cross-cluster federation. With the origin cluster authorized in the
// tenant's peer envelope and a peer that accepts the origin-pull, /source
// resolves to the peer-supplied DTSC URL (remote_source decision) — not push://,
// not "". The caller node (resolved from /source/by-node/) IS recorded as the
// puller in NotifyOriginPull.
func TestHandleGetSourceLbMissArrangesRemoteSource(t *testing.T) {
	withSeededBalancer(t) // local balancer has no node carrying the stream
	withLoggerGetSource(t)

	stack := setupFedArmsStack(t,
		&foghornfederationpb.QueryStreamResponse{
			OriginClusterId: fedArmsOriginCluster,
			Candidates: []*foghornfederationpb.EdgeCandidate{
				{NodeId: "origin-node-1", IsOrigin: true, DtscUrl: "dtsc://origin-edge.example:4200"},
			},
		},
		&foghornfederationpb.OriginPullAck{Accepted: true, DtscUrl: "dtsc://origin-edge.example:4200"},
	)
	fedArmsCommodore(t, "tenant-fed", fedArmsOriginCluster, peerEnvelopeFedArms(fedArmsOriginCluster))

	// Drive through /source/by-node/<caller> so callerNodeID is non-empty; the
	// arrange path fails closed when the puller can't be identified.
	c, w := newSourceRequestGetSource(sourceByNodePathPrefix+"edge-caller-1", url.Values{})
	handleGetSource(c, "live+show", c.Request.URL.Query())

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: body=%q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "dtsc://origin-edge.example:4200" {
		t.Fatalf("remote_source body = %q, want the peer DTSC URL", got)
	}
	if stack.server.gotQueryStream != "live+show" {
		t.Fatalf("peer QueryStream got %q, want live+show", stack.server.gotQueryStream)
	}
	if stack.server.gotNotifyDestNode != "edge-caller-1" {
		t.Fatalf("NotifyOriginPull dest node = %q, want edge-caller-1 (caller IS the puller)", stack.server.gotNotifyDestNode)
	}
	// The arrangement must be durably tracked so a second /source doesn't start a
	// parallel pull: the local registry now holds the replication with the URL.
	loc, ok := control.StreamRegistryInstance.LocalReplication(context.Background(), "show")
	if !ok || loc.PullDTSCURL != "dtsc://origin-edge.example:4200" {
		t.Fatalf("registry replication = (%v,%q), want tracked pull DTSC", ok, loc.PullDTSCURL)
	}
}

// Invariant: when the origin cluster is NOT in the tenant's current peer
// envelope, the front-door reauthorization (AuthoritativeClusterServable)
// refuses to federate — even though Commodore named it the origin. The /source
// answer falls back to the live+ terminal push:// and NO upstream/peer is dialed
// for an origin-pull (nothing leaks to a revoked peer).
func TestHandleGetSourceLbMissRevokedPeerRefuses(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	stack := setupFedArmsStack(t,
		&foghornfederationpb.QueryStreamResponse{
			OriginClusterId: fedArmsOriginCluster,
			Candidates: []*foghornfederationpb.EdgeCandidate{
				{NodeId: "origin-node-1", IsOrigin: true, DtscUrl: "dtsc://origin-edge.example:4200"},
			},
		},
		&foghornfederationpb.OriginPullAck{Accepted: true, DtscUrl: "dtsc://origin-edge.example:4200"},
	)
	// Envelope authorizes a DIFFERENT cluster, not the origin → revoked.
	fedArmsCommodore(t, "tenant-fed", fedArmsOriginCluster, peerEnvelopeFedArms("cluster-some-other"))

	c, w := newSourceRequestGetSource(sourceByNodePathPrefix+"edge-caller-1", url.Values{})
	handleGetSource(c, "live+show", c.Request.URL.Query())

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "push://" {
		t.Fatalf("revoked-peer live+ body = %q, want push:// (federation refused)", got)
	}
	// Refusal happens before any peer RPC: QueryStream never fires.
	if stack.server.gotQueryStream != "" {
		t.Fatalf("peer QueryStream was dialed (%q) for a revoked origin cluster; must refuse before federating", stack.server.gotQueryStream)
	}
	if _, ok := control.StreamRegistryInstance.LocalReplication(context.Background(), "show"); ok {
		t.Fatal("a replication was recorded for a revoked peer; nothing must be arranged")
	}
}

// Invariant: a pull+ stream whose upstream is allowed-cluster-pinned AWAY from
// this cluster (FilterPlacementClusters yields eligible==0) does not 404 — it
// federates to the origin cluster that IS allowed and returns that peer's DTSC
// URL (pull_federated decision). This is the eligible==0 -> federation fallthrough
// arm of handleGetPullSource, distinct from the remote_source live+/native arm.
func TestHandleGetPullSourceNotPlaceableFederates(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	stack := setupFedArmsStack(t,
		&foghornfederationpb.QueryStreamResponse{
			OriginClusterId: fedArmsOriginCluster,
			Candidates: []*foghornfederationpb.EdgeCandidate{
				{NodeId: "origin-pull-node", IsOrigin: true, DtscUrl: "dtsc://pull-origin.example:4200"},
			},
		},
		&foghornfederationpb.OriginPullAck{Accepted: true, DtscUrl: "dtsc://pull-origin.example:4200"},
	)

	// Commodore resolves BOTH the pull-source row (upstream + allowed clusters
	// that EXCLUDE this cluster) and the internal-name origin/peer envelope.
	startFedArmsCommodore(t, &fedArmsCommodoreFake{
		pullSource: func(_ context.Context, _ *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
			return &commodorepb.ResolvePullSourceByInternalNameResponse{
				Found:             true,
				Enabled:           true,
				SourceUri:         "https://upstream.example/live/show.m3u8",
				AllowedClusterIds: []string{fedArmsOriginCluster}, // local cluster NOT allowed
			}, nil
		},
		internalName: func(_ context.Context, _ *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			return &commodorepb.ResolveInternalNameResponse{
				InternalName:    "show",
				TenantId:        "tenant-fed",
				OriginClusterId: fedArmsOriginCluster,
				ClusterPeers:    peerEnvelopeFedArms(fedArmsOriginCluster),
			}, nil
		},
	})

	c, w := newSourceRequestGetSource(sourceByNodePathPrefix+"edge-caller-2", url.Values{})
	// CLUSTER_ID env drives the local placement candidate inside handleGetPullSource;
	// set it to the local cluster so FilterPlacementClusters sees this cluster but
	// allowed_cluster_ids excludes it -> eligible==0 -> federation.
	t.Setenv("CLUSTER_ID", fedArmsLocalCluster)
	handleGetSource(c, "pull+show", c.Request.URL.Query())

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: body=%q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "dtsc://pull-origin.example:4200" {
		t.Fatalf("pull_federated body = %q, want the federated peer DTSC URL (not OfflineNotPlaced)", got)
	}
	if stack.server.gotNotifyDestNode != "edge-caller-2" {
		t.Fatalf("NotifyOriginPull dest = %q, want edge-caller-2", stack.server.gotNotifyDestNode)
	}
}

// Invariant: even on the eligible==0 federation fallthrough, a peer that REJECTS
// the origin-pull (Accepted=false) yields the OfflineNotPlaced sentinel — the
// pull is not silently treated as placed, and no replication is recorded.
func TestHandleGetPullSourcePeerRejectsStaysNotPlaced(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	setupFedArmsStack(t,
		&foghornfederationpb.QueryStreamResponse{
			OriginClusterId: fedArmsOriginCluster,
			Candidates: []*foghornfederationpb.EdgeCandidate{
				{NodeId: "origin-pull-node", IsOrigin: true, DtscUrl: "dtsc://pull-origin.example:4200"},
			},
		},
		&foghornfederationpb.OriginPullAck{Accepted: false, Reason: "capacity"},
	)

	startFedArmsCommodore(t, &fedArmsCommodoreFake{
		pullSource: func(_ context.Context, _ *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
			return &commodorepb.ResolvePullSourceByInternalNameResponse{
				Found:             true,
				Enabled:           true,
				SourceUri:         "https://upstream.example/live/show.m3u8",
				AllowedClusterIds: []string{fedArmsOriginCluster},
			}, nil
		},
		internalName: func(_ context.Context, _ *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			return &commodorepb.ResolveInternalNameResponse{
				InternalName:    "show",
				TenantId:        "tenant-fed",
				OriginClusterId: fedArmsOriginCluster,
				ClusterPeers:    peerEnvelopeFedArms(fedArmsOriginCluster),
			}, nil
		},
	})

	c, w := newSourceRequestGetSource(sourceByNodePathPrefix+"edge-caller-3", url.Values{})
	t.Setenv("CLUSTER_ID", fedArmsLocalCluster)
	handleGetSource(c, "pull+show", c.Request.URL.Query())

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != control.OfflineNotPlaced {
		t.Fatalf("peer-rejected pull body = %q, want %q", got, control.OfflineNotPlaced)
	}
	if _, ok := control.StreamRegistryInstance.LocalReplication(context.Background(), "show"); ok {
		t.Fatal("replication recorded despite peer rejection")
	}
}
