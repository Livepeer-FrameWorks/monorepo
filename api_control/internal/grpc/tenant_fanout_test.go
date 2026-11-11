package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type testTenantControlServer struct {
	foghornpb.UnimplementedTenantControlServiceServer
	terminateResp  *foghorncontrolpb.TerminateTenantStreamsResponse
	invalidateResp *foghorncontrolpb.InvalidateTenantCacheResponse
	invalidateErr  error
	terminateWait  bool
	invalidateWait bool
	invalidated    chan struct{}
	terminated     chan struct{}
}

func (s *testTenantControlServer) TerminateTenantStreams(ctx context.Context, _ *foghorncontrolpb.TerminateTenantStreamsRequest) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
	if s.terminateWait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.terminated != nil {
		close(s.terminated)
	}
	return s.terminateResp, nil
}

func TestTerminateTenantStreams_HangingCellDoesNotStarveHealthyCell(t *testing.T) {
	hanging := startTenantControlTestServer(t, &testTenantControlServer{terminateWait: true})
	terminated := make(chan struct{})
	healthy := startTenantControlTestServer(t, &testTenantControlServer{
		terminated:    terminated,
		terminateResp: &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: 2, SessionsTerminated: 3},
	})
	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterID: "hanging", foghornAddr: hanging,
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "healthy", FoghornGrpcAddr: healthy}},
		resolvedAt:   time.Now(),
	})
	ctx, cancel := context.WithTimeout(serviceCtx(), 250*time.Millisecond)
	defer cancel()
	_, err := server.TerminateTenantStreams(ctx, &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1"})
	select {
	case <-terminated:
	default:
		t.Fatal("healthy cell was never stopped because the first cell consumed the caller deadline")
	}
	progress := grpcutil.StreamStopProgress(err)
	if status.Code(err) != codes.Unavailable || progress.GetStreamsTerminated() != 2 || progress.GetSessionsTerminated() != 3 {
		t.Fatalf("pending stop lost healthy-cell progress: response=%v err=%v", progress, err)
	}
}

func TestTerminateTenantStreams_IncludesRecordedDeentitledCells(t *testing.T) {
	for _, reachable := range []bool{true, false} {
		t.Run(map[bool]string{true: "reachable", false: "missing address"}[reachable], func(t *testing.T) {
			primary := startTenantControlTestServer(t, &testTenantControlServer{terminateResp: &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: 1}})
			server := newTenantFanoutTestServer(t, &clusterRoute{clusterID: "current", foghornAddr: primary, resolvedAt: time.Now()})
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			server.db = db
			mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant-1", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("historical"))
			stopped := make(chan struct{})
			entry := &foghornDiscoveryEntry{at: time.Now()}
			if reachable {
				entry.addrs = []string{startTenantControlTestServer(t, &testTenantControlServer{terminated: stopped, terminateResp: &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: 1}})}
			}
			server.foghornDiscovery.Store("historical", entry)
			response, callErr := server.TerminateTenantStreams(serviceCtx(), &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1"})
			if reachable {
				select {
				case <-stopped:
				default:
					t.Fatal("recorded cell lost from entitlement route was never stopped")
				}
				if callErr != nil || response.GetStreamsTerminated() != 2 {
					t.Fatalf("historical stop response=%v err=%v", response, callErr)
				}
			} else {
				if status.Code(callErr) != codes.Unavailable || grpcutil.StreamStopProgress(callErr).GetStreamsTerminated() != 1 {
					t.Fatalf("missing historical cell was acknowledged: %v", callErr)
				}
				if _, cached := server.routeCache["tenant-1"]; cached {
					t.Fatal("failed delivery retained the stale tenant route")
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func (s *testTenantControlServer) InvalidateTenantCache(ctx context.Context, _ *foghorncontrolpb.InvalidateTenantCacheRequest) (*foghorncontrolpb.InvalidateTenantCacheResponse, error) {
	if s.invalidateWait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.invalidated != nil {
		close(s.invalidated)
	}
	return s.invalidateResp, s.invalidateErr
}

func startTenantControlTestServer(t *testing.T, svc foghornpb.TenantControlServiceServer) string {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	foghornpb.RegisterTenantControlServiceServer(srv, svc)
	go func() {
		_ = srv.Serve(lis)
	}()

	t.Cleanup(func() {
		srv.GracefulStop()
		_ = lis.Close()
	})

	return lis.Addr().String()
}

func newTenantFanoutTestServer(t *testing.T, route *clusterRoute) *CommodoreServer {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("ListAccountIngestCells").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}))
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logrus.New()})
	t.Cleanup(func() { _ = pool.Close() })

	server := &CommodoreServer{
		db:            db,
		logger:        logrus.New(),
		foghornPool:   pool,
		routeCache:    map[string]*clusterRoute{"tenant-1": route},
		routeCacheTTL: time.Minute,
	}
	server.controlRecipientLookup = func(_ context.Context, request *quartermasterpb.GetTenantControlRecipientsRequest) (*quartermasterpb.GetTenantControlRecipientsResponse, error) {
		response := &quartermasterpb.GetTenantControlRecipientsResponse{Complete: true}
		targets := buildClusterFanoutTargets(route)
		seen := map[string]bool{}
		for _, target := range targets {
			if target.clusterID != "" && !seen[target.clusterID] {
				response.Recipients = append(response.Recipients, &quartermasterpb.TenantControlRecipient{CellId: target.clusterID})
				seen[target.clusterID] = true
			}
			if target.addr != "" {
				server.foghornDiscovery.Store(target.clusterID, &foghornDiscoveryEntry{at: time.Now(), addrs: []string{target.addr}})
			}
		}
		for cell := range missingTenantFanoutCells(route, targets) {
			if !seen[cell] {
				response.Recipients = append(response.Recipients, &quartermasterpb.TenantControlRecipient{CellId: cell})
			}
		}
		for _, cell := range request.GetHistoricalClusterIds() {
			if !seen[cell] {
				response.Recipients = append(response.Recipients, &quartermasterpb.TenantControlRecipient{CellId: cell, SourceClusterIds: []string{cell}})
				seen[cell] = true
			}
		}

		return response, nil
	}
	return server
}

func TestTerminateTenantStreams_FailsWhenAllDialAttemptsFail(t *testing.T) {
	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "bad-cluster", FoghornGrpcAddr: "bad host:50051"}},
		resolvedAt:   time.Now(),
	})

	_, err := server.TerminateTenantStreams(serviceCtx(), &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1", Reason: "suspended"})
	if err == nil {
		t.Fatal("expected error")
	}
	st := status.Convert(err)
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected unavailable, got %v", st.Code())
	}
}

func TestTerminateTenantStreams_PartialResultsRemainPending(t *testing.T) {
	goodAddr := startTenantControlTestServer(t, &testTenantControlServer{
		terminateResp: &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: 2, SessionsTerminated: 3, StreamNames: []string{"a", "b"}},
	})

	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "cluster-ok", FoghornGrpcAddr: goodAddr},
			{ClusterId: "cluster-down", FoghornGrpcAddr: "127.0.0.1:1"},
		},
		resolvedAt: time.Now(),
	})

	ctx, cancel := context.WithTimeout(serviceCtx(), 150*time.Millisecond)
	defer cancel()

	_, err := server.TerminateTenantStreams(ctx, &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1", Reason: "suspended"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("partial delivery must stay pending: %v", err)
	}
	resp := grpcutil.StreamStopProgress(err)
	if resp == nil {
		t.Fatalf("missing partial counts: %v", err)
	}
	if resp.StreamsTerminated != 2 {
		t.Fatalf("expected 2 streams terminated from successful cluster, got %d", resp.StreamsTerminated)
	}
}

func TestInvalidateTenantCache_DeduplicatesTargets(t *testing.T) {
	goodAddr := startTenantControlTestServer(t, &testTenantControlServer{
		invalidateResp: &foghorncontrolpb.InvalidateTenantCacheResponse{EntriesInvalidated: 5},
	})

	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterID:   "cluster-primary",
		foghornAddr: goodAddr,
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "cluster-primary", FoghornGrpcAddr: goodAddr},
			{ClusterId: "cluster-primary", FoghornGrpcAddr: goodAddr},
		},
		resolvedAt: time.Now(),
	})

	resp, err := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1", Reason: "reactivated"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EntriesInvalidated != 5 {
		t.Fatalf("expected single target invalidation count, got %d", resp.EntriesInvalidated)
	}
}

func TestBuildClusterFanoutTargetsDeduplicatesAddressOnlyPeers(t *testing.T) {
	addr := "foghorn.internal:18019"
	targets := buildClusterFanoutTargets(&clusterRoute{foghornAddr: addr, clusterPeers: []*clusterpeerpb.TenantClusterPeer{{FoghornGrpcAddr: addr}, {FoghornGrpcAddr: addr}}})
	if len(targets) != 1 || targets[0].addr != addr {
		t.Fatalf("legacy address-only peer deduplication=%v", targets)
	}
}

func TestTenantControlRejectsRecipientWithoutCanonicalCell(t *testing.T) {
	server := newTenantFanoutTestServer(t, &clusterRoute{})
	server.controlRecipientLookup = func(_ context.Context, request *quartermasterpb.GetTenantControlRecipientsRequest) (*quartermasterpb.GetTenantControlRecipientsResponse, error) {
		return &quartermasterpb.GetTenantControlRecipientsResponse{Complete: true, Recipients: []*quartermasterpb.TenantControlRecipient{{}}}, nil
	}
	if _, err := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("malformed inventory became authoritative empty: %v", err)
	}
}

func TestInvalidateTenantCache_NoTargetsIsSuccessful(t *testing.T) {
	server := newTenantFanoutTestServer(t, &clusterRoute{resolvedAt: time.Now()})
	response, err := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1", Reason: "tenant closed"})
	if err != nil || response.GetEntriesInvalidated() != 0 {
		t.Fatalf("closed tenant cache invalidation=%v err=%v", response, err)
	}
}

func TestTerminateTenantStreamsAccountsForEveryEntitledCell(t *testing.T) {
	for _, test := range []struct {
		name  string
		peers []*clusterpeerpb.TenantClusterPeer
		want  codes.Code
	}{
		{"no clusters", nil, codes.OK},
		{"unhealthy entitled cell without discovery", []*clusterpeerpb.TenantClusterPeer{{ClusterId: "unhealthy"}}, codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTenantFanoutTestServer(t, &clusterRoute{admissionPeers: test.peers, resolvedAt: time.Now()})
			_, err := server.TerminateTenantStreams(serviceCtx(), &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1"})
			if status.Code(err) != test.want {
				t.Fatalf("stop result=%v want=%s", err, test.want)
			}
		})
	}
	t.Run("healthy cell stopped but unhealthy cell undiscovered", func(t *testing.T) {
		good := startTenantControlTestServer(t, &testTenantControlServer{terminateResp: &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: 2}})
		server := newTenantFanoutTestServer(t, &clusterRoute{clusterID: "healthy", foghornAddr: good,
			admissionPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "healthy"}, {ClusterId: "unhealthy"}}, resolvedAt: time.Now()})
		_, err := server.TerminateTenantStreams(serviceCtx(), &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1"})
		if status.Code(err) != codes.Unavailable || grpcutil.StreamStopProgress(err).GetStreamsTerminated() != 2 {
			t.Fatalf("incomplete stop lost pending status or successful counts: %v", err)
		}
	})
}

func TestInvalidateTenantCacheDiscoveryGapIsNotEmptyTenant(t *testing.T) {
	for _, test := range []struct {
		name  string
		route *clusterRoute
		want  codes.Code
	}{
		{"no clusters", &clusterRoute{}, codes.OK},
		{"primary without discovery", &clusterRoute{clusterID: "cell"}, codes.Unavailable},
		{"official without discovery", &clusterRoute{officialClusterID: "cell"}, codes.Unavailable},
		{"unhealthy entitlement", &clusterRoute{admissionPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cell"}}}, codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.route.resolvedAt = time.Now()
			server := newTenantFanoutTestServer(t, test.route)
			_, callErr := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1"})
			if status.Code(callErr) != test.want {
				t.Fatalf("invalidation result=%v want=%s", callErr, test.want)
			}
		})
	}
}

func TestInvalidateTenantCacheRefusesPartialDelivery(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "undiscoverable cell", false: "failed RPC"}[missing], func(t *testing.T) {
			good := startTenantControlTestServer(t, &testTenantControlServer{invalidateResp: &foghorncontrolpb.InvalidateTenantCacheResponse{EntriesInvalidated: 5}})
			bad := ""
			if !missing {
				bad = startTenantControlTestServer(t, &testTenantControlServer{invalidateErr: status.Error(codes.Unavailable, "cell disconnected")})
			}
			server := newTenantFanoutTestServer(t, &clusterRoute{clusterID: "good", foghornAddr: good, clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "bad", FoghornGrpcAddr: bad}}, resolvedAt: time.Now()})
			if _, err := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1"}); status.Code(err) != codes.Unavailable {
				t.Fatalf("partial delivery reported success: %v", err)
			}
		})
	}
}

func TestTenantStopUsesBothRecipientSourcesIndependently(t *testing.T) {
	for _, source := range []string{"no entitlements", "route unavailable", "recorded inventory unavailable", "route hangs", "recorded inventory hangs"} {
		t.Run(source, func(t *testing.T) {
			stopped := make(chan struct{})
			addr := startTenantControlTestServer(t, &testTenantControlServer{terminated: stopped, terminateResp: &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: 2}})
			server := newTenantFanoutTestServer(t, &clusterRoute{clusterID: "recorded", foghornAddr: addr, resolvedAt: time.Now()})
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			server.db = db
			if source == "recorded inventory unavailable" || source == "recorded inventory hangs" {
				expect := mock.ExpectQuery("ListAccountIngestCells")
				if source == "recorded inventory hangs" {
					expect.WillDelayFor(2 * time.Second)
				}
				expect.WillReturnError(status.Error(codes.Unavailable, "inventory unavailable"))
			} else {
				server.controlRecipientLookup = func(ctx context.Context, _ *quartermasterpb.GetTenantControlRecipientsRequest) (*quartermasterpb.GetTenantControlRecipientsResponse, error) {
					if source == "no entitlements" {
						return &quartermasterpb.GetTenantControlRecipientsResponse{Complete: true}, nil
					}
					if source == "route hangs" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return nil, status.Error(codes.Unavailable, "owner inventory unavailable")
				}
				mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant-1", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("recorded"))
				server.foghornDiscovery.Store("recorded", &foghornDiscoveryEntry{at: time.Now(), addrs: []string{addr}})
			}
			ctx, cancel := context.WithTimeout(serviceCtx(), time.Second)
			defer cancel()
			response, callErr := server.TerminateTenantStreams(ctx, &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1"})
			select {
			case <-stopped:
			default:
				t.Fatal("recipient-source failure prevented delivery to a known cell")
			}
			if source == "no entitlements" {
				if callErr != nil || response.GetStreamsTerminated() != 2 {
					t.Fatalf("authoritative empty entitlement did not deliver recorded cells: %v %v", response, callErr)
				}
			} else if status.Code(callErr) != codes.Unavailable || grpcutil.StreamStopProgress(callErr).GetStreamsTerminated() != 2 {
				t.Fatalf("incomplete recipient inventory lost retry or progress: %v", callErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCacheInvalidationHangingCellDoesNotStarveHealthyCell(t *testing.T) {
	hanging := startTenantControlTestServer(t, &testTenantControlServer{invalidateWait: true})
	invalidated := make(chan struct{})
	healthy := startTenantControlTestServer(t, &testTenantControlServer{invalidated: invalidated, invalidateResp: &foghorncontrolpb.InvalidateTenantCacheResponse{EntriesInvalidated: 3}})
	server := newTenantFanoutTestServer(t, &clusterRoute{clusterID: "hanging", foghornAddr: hanging, clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "healthy", FoghornGrpcAddr: healthy}}, resolvedAt: time.Now()})
	ctx, cancel := context.WithTimeout(serviceCtx(), 250*time.Millisecond)
	defer cancel()
	_, err := server.InvalidateTenantCache(ctx, &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1"})
	select {
	case <-invalidated:
	default:
		t.Fatal("hanging cell prevented healthy-cell cache invalidation")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("partial invalidation was acknowledged: %v", err)
	}
}
