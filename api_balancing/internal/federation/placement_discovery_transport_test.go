package federation

import (
	"context"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPlacementDiscoveryLocalAndAuthenticatedRemoteObservationsMatch(t *testing.T) {
	f, _, _ := livePathFixture(t)
	f.snapshot.Nodes[0].OutputsObservedAt = f.now.Add(-30 * time.Second)
	f.snapshot.Nodes[1].Outputs = map[string]any{"HTTP": "http://HOST:8080/$.html"}
	prepares := 0
	var queries, geographicQueries atomic.Int32
	destination := placementRPCFixture{
		query: func(ctx context.Context, query *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error) {
			queries.Add(1)
			if query.TenantId != f.query.TenantId || query.ObjectId != f.query.ObjectId || query.InternalName != f.query.InternalName {
				return nil, status.Error(codes.FailedPrecondition, "fixture authority does not cover this identity")
			}
			if query.ClientLocation != nil {
				geographicQueries.Add(1)
			}
			return f.discovery.QueryPlacementCandidates(ctx, query)
		},
		prepare: func(ctx context.Context, req *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error) {
			prepares++
			if deadline, ok := ctx.Deadline(); !ok || deadline.After(req.GetExpiresAt().AsTime()) {
				t.Error("destination dispatch outlived decision evidence")
			}
			return preparationWireResponse(req), nil
		},
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: "placement-test-service", MetadataPolicy: middleware.MetadataPolicyDeny,
	})))
	NewFederationServer(FederationServerConfig{ClusterID: "us-registry-cluster", ControlCellID: "us-cell", Placement: destination, AllowFederationMutations: true}).RegisterServices(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	client, err := foghorn.NewGRPCClient(foghorn.GRPCConfig{
		GRPCAddr: listener.Addr().String(), ServiceToken: "placement-test-service", AllowInsecure: true, Logger: newFederationTestLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	pool := &fakePool{client: client}
	peerCache, _ := setupTestCache(t)
	if err = peerCache.PublishPeerHints(context.Background(), "quartermaster", map[string]PeerHint{
		"us-virtual": {Addr: listener.Addr().String(), ControlCellID: "us-cell", AlwaysOn: true},
		"us-cell":    {Addr: "wrong-cluster-namespace:18019", AlwaysOn: true},
	}); err != nil {
		t.Fatal(err)
	}
	peers := newTestPeerManager(t, "eu-registry-cluster", peerCache, false)
	if err = peers.loadPeerAddressesFromRedis(); err != nil {
		t.Fatal(err)
	}
	transport := PlacementTransport{
		LocalCellID: "us-cell", Local: destination, Client: NewFederationClient(FederationClientConfig{Pool: pool}),
		CellAddress: peers.GetControlCellAddr,
	}
	cell := balancer.PlacementCell{ID: "us-cell", ClusterIDs: []string{"empty", "us"}}
	req := balancer.PlacementRouteRequest{TenantID: f.query.TenantId, ObjectID: f.query.ObjectId, InternalName: f.query.InternalName,
		Verb: placement.Serve, Protocol: "hls", PolicyDigest: f.query.PolicyDigest, PolicyRevision: f.query.PolicyRevision,
		ParentRevision: f.query.ParentRevision, SourceGeneration: f.query.SourceGeneration}
	local, err := transport.Observe(context.Background(), cell, req)
	if err != nil {
		t.Fatal(err)
	}
	transport.LocalCellID = "eu-cell"
	// An incoming viewer bearer must not displace the federation service identity.
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "viewer-bearer-not-a-service-token")
	remote, err := transport.Observe(ctx, cell, req)
	if err != nil || !reflect.DeepEqual(local, remote) || len(remote.Candidates) != 12 || prepares != 0 {
		t.Fatalf("local/remote discovery diverged or prepared media: local=%+v remote=%+v err=%v prepares=%d", local, remote, err, prepares)
	}
	if pool.gotClusterID != "us-cell" || pool.gotAddr != listener.Addr().String() {
		t.Fatal("transport lost owning-cell destination")
	}
	transport.Observations = NewPlacementObservationCache(func() time.Time { return f.now })
	req.TenantAuthorityVersion, req.ObjectAuthorityVersion = f.pair.Tenant.Version, f.pair.Object.Version
	beforeQueries := queries.Load()
	for _, longitude := range []float64{-74, 5} {
		req.Location = &placement.Coordinates{Latitude: 50, Longitude: longitude}
		cached, queryErr := transport.Observe(ctx, cell, req)
		if queryErr != nil || !reflect.DeepEqual(cached, remote) || req.Location.Longitude != longitude {
			t.Fatalf("remote observation reuse changed facts or client location: %v", queryErr)
		}
	}
	if queries.Load() != beforeQueries+1 || geographicQueries.Load() != 0 {
		t.Fatalf("viewer geography repeated/pruned peer census: queries=%d geographic=%d", queries.Load()-beforeQueries, geographicQueries.Load())
	}
	for _, changed := range []string{"tenant authority", "object authority", "peer address"} {
		switch changed {
		case "tenant authority":
			req.TenantAuthorityVersion++
		case "object authority":
			req.ObjectAuthorityVersion++
		case "peer address":
			transport.CellAddress = func(string) string { return "replacement-peer:18019" }
		}
		beforeQueries = queries.Load()
		if _, queryErr := transport.Observe(ctx, cell, req); queryErr != nil || queries.Load() != beforeQueries+1 {
			t.Fatalf("changed %s reused old census: queries=%d err=%v", changed, queries.Load()-beforeQueries, queryErr)
		}
	}
	transport.CellAddress = peers.GetControlCellAddr
	req.Location = nil
	for _, changed := range []string{"tenant", "object", "internal name", "protocol", "source", "policy digest", "policy revision", "parent revision"} {
		other := req
		switch changed {
		case "tenant":
			other.TenantID = "another-tenant"
		case "object":
			other.ObjectID = "live_stream:another-stream"
		case "internal name":
			other.InternalName = "another-internal"
		case "protocol":
			other.Protocol = "dash"
		case "source":
			other.SourceGeneration = "another-generation"
		case "policy digest":
			other.PolicyDigest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		case "policy revision":
			other.PolicyRevision++
		case "parent revision":
			other.ParentRevision++
		}
		beforeQueries = queries.Load()
		_, _ = transport.Observe(ctx, cell, other)
		if queries.Load() != beforeQueries+1 {
			t.Fatalf("changed %s reused another query's evidence", changed)
		}
	}
	decision, err := placement.Evaluate(placement.Request{TenantID: req.TenantID, Verb: req.Verb, Now: f.now, Candidates: remote.Candidates, Complete: remote.Complete})
	if err != nil || len(decision.Choices) != 10 || decision.Assessments[0].Reason != placement.StaleTelemetry || decision.Assessments[1].Reason != placement.NodeUnavailable {
		t.Fatalf("transport replaced listener evidence or discarded healthy neighbors: %+v, %v", decision, err)
	}
	// The delegate acknowledges only the contract here; it does not prepare media.
	attempt := balancer.PlacementPreparationRequest{Route: req, Choice: decision.Choices[0],
		AttemptID: testPreparationAttempt(t), ExpiresAt: time.Now().Add(5 * time.Second)}
	transport.LocalCellID = "us-cell"
	localPrepared, err := transport.Prepare(ctx, cell, attempt)
	if err != nil {
		t.Fatal(err)
	}
	transport.LocalCellID = "eu-cell"
	remotePrepared, err := transport.Prepare(ctx, cell, attempt)
	if err != nil || !reflect.DeepEqual(localPrepared, remotePrepared) || prepares != 2 || !remotePrepared.ExpiresAt.Equal(attempt.ExpiresAt) {
		t.Fatalf("local/remote preparation contract diverged: %+v, %+v, %v", localPrepared, remotePrepared, err)
	}
	attempt.ExpiresAt = time.Now().Add(-time.Second)
	for _, coordinator := range []string{"us-cell", "eu-cell"} {
		transport.LocalCellID = coordinator
		if _, err := transport.Prepare(ctx, cell, attempt); err == nil || prepares != 2 {
			t.Fatalf("expired attempt reached %s destination: %v (%d)", coordinator, err, prepares)
		}
	}
}

func TestPlacementDiscoveryStartupWiringDoesNotActivatePreparation(t *testing.T) {
	f, _, _ := livePathFixture(t)
	destination := &PlacementDestination{}
	server := NewFederationServer(FederationServerConfig{Placement: destination, AllowFederationMutations: true})
	if response, err := server.QueryPlacementCandidates(svcAuthCtx(), f.query); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("missing startup dependencies produced discovery: %v, %v", response, err)
	}
	destination.Discovery = f.discovery
	if response, err := server.QueryPlacementCandidates(svcAuthCtx(), f.query); err != nil || !response.GetComplete() || len(response.GetCandidates()) != 12 {
		t.Fatalf("configured destination did not expose inventory: %v, %v", response, err)
	}
	if response, err := server.QueryPlacementCandidates(context.Background(), f.query); response != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("discovery wiring bypassed authentication: %v, %v", response, err)
	}
	req := &placementpb.PreparePlacementRequest{Query: f.query, ClusterId: "us", NodeId: "node-00",
		AttemptId: testPreparationAttempt(t), ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Second))}
	if response, err := server.PreparePlacement(svcAuthCtx(), req); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("discovery readiness became media preparation permission: %v, %v", response, err)
	}
	f.pair.Tenant.Ready = false
	if response, err := server.QueryPlacementCandidates(svcAuthCtx(), f.query); response != nil || err == nil {
		t.Fatalf("authority loss returned old inventory: %v, %v", response, err)
	}
}
