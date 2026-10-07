package main

import (
	"context"
	"database/sql"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// originCell is the peer cell the wired server talks to over real gRPC: it has
// deleted every artifact it is asked about and holds no tenant artifacts.
type originCell struct {
	foghornfederationpb.UnimplementedFoghornFederationServer
	mu       sync.Mutex
	prepared []string
	listed   []string
}

func (o *originCell) PrepareArtifact(_ context.Context, req *foghornfederationpb.PrepareArtifactRequest) (*foghornfederationpb.PrepareArtifactResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prepared = append(o.prepared, req.GetArtifactId())
	return &foghornfederationpb.PrepareArtifactResponse{Error: control.PrepareArtifactNotFoundRefusal}, nil
}

func (o *originCell) ListTenantArtifacts(_ context.Context, req *foghornfederationpb.ListTenantArtifactsRequest) (*foghornfederationpb.ListTenantArtifactsResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.listed = append(o.listed, req.GetTenantId())
	return &foghornfederationpb.ListTenantArtifactsResponse{}, nil
}

func startOriginCell(t *testing.T) (*originCell, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	origin := &originCell{}
	srv := grpc.NewServer()
	foghornfederationpb.RegisterFoghornFederationServer(srv, origin)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	return origin, listener.Addr().String()
}

// quartermasterCensus answers the peer census the way Quartermaster does.
type quartermasterCensus struct {
	peers []*quartermasterpb.PeerCluster
}

func (q quartermasterCensus) ListPeers(context.Context, string) (*quartermasterpb.ListPeersResponse, error) {
	return &quartermasterpb.ListPeersResponse{Peers: q.peers}, nil
}

type localArtifacts struct{}

func (localArtifacts) CreateClip(context.Context, *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
	return &sharedpb.CreateClipResponse{}, nil
}

func (localArtifacts) StartDVR(context.Context, *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
	return &sharedpb.StartDVRResponse{}, nil
}

func (localArtifacts) DeleteClip(context.Context, *sharedpb.DeleteClipRequest) (*sharedpb.DeleteClipResponse, error) {
	return &sharedpb.DeleteClipResponse{}, nil
}

func (localArtifacts) StopDVR(context.Context, *sharedpb.StopDVRRequest) (*sharedpb.StopDVRResponse, error) {
	return &sharedpb.StopDVRResponse{}, nil
}

func (localArtifacts) DeleteDVR(context.Context, *sharedpb.DeleteDVRRequest) (*sharedpb.DeleteDVRResponse, error) {
	return &sharedpb.DeleteDVRResponse{}, nil
}

func (localArtifacts) DeleteVodAsset(context.Context, *sharedpb.DeleteVodAssetRequest) (*sharedpb.DeleteVodAssetResponse, error) {
	return &sharedpb.DeleteVodAssetResponse{}, nil
}

type peerChannel struct {
	ctx      context.Context
	messages []*foghornfederationpb.PeerMessage
}

func (p *peerChannel) Send(*foghornfederationpb.PeerMessage) error { return nil }
func (p *peerChannel) Recv() (*foghornfederationpb.PeerMessage, error) {
	if len(p.messages) == 0 {
		return nil, io.EOF
	}
	msg := p.messages[0]
	p.messages = p.messages[1:]
	return msg, nil
}
func (p *peerChannel) SetHeader(metadata.MD) error  { return nil }
func (p *peerChannel) SendHeader(metadata.MD) error { return nil }
func (p *peerChannel) SetTrailer(metadata.MD)       {}
func (p *peerChannel) Context() context.Context     { return p.ctx }
func (p *peerChannel) SendMsg(any) error            { return nil }
func (p *peerChannel) RecvMsg(any) error            { return nil }

func wiredServerConfig(db *sql.DB) federation.FederationServerConfig {
	logger := logging.NewLogger()
	return federation.FederationServerConfig{
		Logger:          logger,
		LB:              balancer.NewLoadBalancer(logger),
		ClusterID:       "cluster-a",
		ControlCellID:   "cell-a",
		DB:              db,
		Placement:       &federation.PlacementDestination{},
		ClipCreator:     localArtifacts{},
		DVRCreator:      localArtifacts{},
		ArtifactHandler: localArtifacts{},
		AdvertisedBacking: func(context.Context, string, string) (federation.S3Backing, bool) {
			return federation.S3Backing{}, false
		},
		IsServedCluster: control.IsServedCluster,
	}
}

// The inbound federation server is built through the same two functions main
// uses, over a real peer manager, federation client and connection pool. Every
// path that depends on peer identity or a peer call must be live: a peer's
// tenant scope and control cell are enforced on what it sends, a pointer to an
// artifact its origin deleted is retired, and metadata migration reaches the
// source cell.
func TestFederationServerWiredFromPeers(t *testing.T) {
	origin, originAddr := startOriginCell(t)
	redis := miniredis.RunT(t)
	logger := logging.NewLogger()
	peers := newFederationPeers(federationPeersConfig{
		ClusterID:     "cluster-a",
		ControlCellID: "cell-a",
		InstanceID:    "foghorn-a-1",
		OwnerTenantID: "owner-tenant",
		Redis:         goredis.NewClient(&goredis.Options{Addr: redis.Addr()}),
		Discovery: quartermasterCensus{peers: []*quartermasterpb.PeerCluster{{
			ClusterId: "cluster-b", FoghornAddr: originAddr, ControlCellId: "cell-b", SharedTenantIds: []string{"tenant-b"},
		}}},
		Pool:   federationFoghornPoolConfig("service-token", logger, true, "", ""),
		Logger: logger,
	})
	t.Cleanup(peers.Close)

	deadline := time.Now().Add(15 * time.Second)
	for peers.peerManager.GetPeerAddr("cluster-b") == "" {
		if time.Now().After(deadline) {
			t.Fatal("peer manager never loaded cluster-b from the census")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Positive knowledge of the peer's control cell comes from an admitted
	// stream's membership.
	if _, err := peers.peerManager.TrackStream(context.Background(), "tracked", "tenant-b", "generation", 1, []control.AdmissionPeerHint{{
		ClusterID: "cluster-b", Addr: originAddr, ControlCellID: "cell-b",
	}}); err != nil {
		t.Logf("tracked stream not yet ready on cluster-b: %v", err)
	}
	if cell, ok := peers.peerManager.PeerControlCell("cluster-b"); !ok || cell != "cell-b" {
		t.Fatalf("peer manager control cell for cluster-b = %q, %v", cell, ok)
	}

	db, mock, mockErr := sqlmock.New()
	if mockErr != nil {
		t.Fatalf("sqlmock.New: %v", mockErr)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv, buildErr := newFederationServer(wiredServerConfig(db), peers, nil)
	if buildErr != nil {
		t.Fatalf("newFederationServer: %v", buildErr)
	}

	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-a", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	serviceCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	lifecycle := func(stream, tenant string) *foghornfederationpb.PeerMessage {
		return &foghornfederationpb.PeerMessage{ClusterId: "cluster-b", Payload: &foghornfederationpb.PeerMessage_StreamLifecycle{
			StreamLifecycle: &foghornfederationpb.StreamLifecycleEvent{
				InternalName: stream, TenantId: tenant, ClusterId: "cluster-b", IsLive: true, SourceRevision: 1,
			},
		}}
	}
	advertisement := func(stream, cell string) *foghornfederationpb.PeerMessage {
		return &foghornfederationpb.PeerMessage{ClusterId: "cluster-b", Payload: &foghornfederationpb.PeerMessage_StreamAd{
			StreamAd: &foghornfederationpb.StreamAdvertisement{
				InternalName: stream, TenantId: "tenant-b", ControlCellId: cell, IsLive: true, Timestamp: time.Now().Unix(),
			},
		}}
	}
	if err := srv.PeerChannel(&peerChannel{ctx: serviceCtx, messages: []*foghornfederationpb.PeerMessage{
		lifecycle("foreign-tenant", "tenant-x"),
		lifecycle("carried-tenant", "tenant-b"),
		advertisement("foreign-cell", "cell-c"),
		advertisement("own-cell", "cell-b"),
	}}); err != nil {
		t.Fatalf("PeerChannel: %v", err)
	}

	if live, err := peers.cache.GetRemoteLiveStream(serviceCtx, "tenant-x", "foreign-tenant"); err != nil || live != nil {
		t.Fatalf("lifecycle for a tenant cluster-b does not carry was applied: %+v, %v", live, err)
	}
	if live, err := peers.cache.GetRemoteLiveStream(serviceCtx, "tenant-b", "carried-tenant"); err != nil || live == nil {
		t.Fatalf("lifecycle for a tenant cluster-b carries was not applied: %+v, %v", live, err)
	}
	if _, err := control.StreamRegistryInstance.ResolveSourceByInternalName(serviceCtx, "foreign-cell"); err == nil {
		t.Fatal("advertisement naming a control cell cluster-b does not belong to was filed")
	}
	if _, err := control.StreamRegistryInstance.ResolveSourceByInternalName(serviceCtx, "own-cell"); err != nil {
		t.Fatalf("advertisement naming cluster-b's own control cell was refused: %v", err)
	}

	// The non-owning cell retires its pointer once the origin, asked over the
	// wired client, confirms the deletion.
	const tenant, hash = "00000000-0000-4000-8000-0000000000e1", "deletedclip00000000000000000001"
	mock.ExpectQuery("GetLiveFederatedArtifactPointer").WithArgs(hash, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "origin_cluster_id"}).AddRow("clip", "cluster-b"))
	mock.ExpectBegin()
	mock.ExpectExec("TombstoneFederatedArtifact").WithArgs(hash, tenant).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SettleFederatedArtifactCatalogRevision").WithArgs(hash, tenant).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	retired, retireErr := srv.ForwardArtifactCommand(serviceCtx, &foghornfederationpb.ForwardArtifactCommandRequest{
		Command: federation.RetireArtifactPointerCommand, ArtifactHash: hash, TenantId: tenant,
	})
	if retireErr != nil || !retired.GetHandled() {
		t.Fatalf("retire of a pointer the origin deleted = %+v, %v; want handled", retired, retireErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pointer was not retired: %v", err)
	}

	migrated, migrateErr := srv.MigrateArtifactMetadata(serviceCtx, &foghornfederationpb.MigrateArtifactMetadataRequest{
		TenantId: "tenant-b", SourceClusterId: "cluster-b",
	})
	if migrateErr != nil || migrated.GetError() != "" {
		t.Fatalf("metadata migration from cluster-b = %+v, %v", migrated, migrateErr)
	}

	origin.mu.Lock()
	defer origin.mu.Unlock()
	if len(origin.prepared) != 1 || origin.prepared[0] != hash {
		t.Fatalf("origin asked to confirm %v, want [%s]", origin.prepared, hash)
	}
	if len(origin.listed) != 1 || origin.listed[0] != "tenant-b" {
		t.Fatalf("origin asked to list %v, want [tenant-b]", origin.listed)
	}
}

// Main refuses to start a federation server that would serve peers without a
// dependency, rather than serving with the dependent checks skipped.
func TestFederationServerRefusesMissingDependency(t *testing.T) {
	redis := miniredis.RunT(t)
	logger := logging.NewLogger()
	peers := newFederationPeers(federationPeersConfig{
		ClusterID: "cluster-a", InstanceID: "foghorn-a-1",
		Redis:     goredis.NewClient(&goredis.Options{Addr: redis.Addr()}),
		Discovery: quartermasterCensus{},
		Pool:      federationFoghornPoolConfig("service-token", logger, true, "", ""),
		Logger:    logger,
	})
	t.Cleanup(peers.Close)
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := wiredServerConfig(db)
	cfg.ArtifactHandler = nil
	if srv, err := newFederationServer(cfg, peers, nil); err == nil || srv != nil {
		t.Fatalf("server without an artifact handler was built: %v", err)
	}
	cfg = wiredServerConfig(db)
	cfg.DB = nil
	if srv, err := newFederationServer(cfg, peers, nil); err == nil || srv != nil {
		t.Fatalf("server without a database was built: %v", err)
	}
}
