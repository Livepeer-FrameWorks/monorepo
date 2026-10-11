package grpc

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	fwserver "github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

type servicePeerStream struct {
	grpc.ServerStream
	slowSends          *atomic.Int32
	firstSlowSent      chan struct{}
	blockedSendEntered chan struct{}
	blockedSendExited  chan struct{}
}

func (s servicePeerStream) Context() context.Context {
	return context.WithValue(s.ServerStream.Context(), ctxkeys.KeyAuthType, "service")
}

// SendMsg marks the slow reader's snapshots, the only ones with peers. gRPC
// admits a message while the stream still has write quota, however large, so
// the first snapshot returns at once and spends the quota; the second one is
// the Send that waits for the unread stream.
func (s servicePeerStream) SendMsg(msg any) error {
	if response, ok := msg.(*quartermasterpb.ListPeersResponse); ok && len(response.Peers) != 0 {
		switch s.slowSends.Add(1) {
		case 1:
			defer close(s.firstSlowSent)
		case 2:
			close(s.blockedSendEntered)
			defer close(s.blockedSendExited)
		}
	}
	return s.ServerStream.SendMsg(msg)
}

func TestWatchPeersDrainPreservesOrdinaryRPC(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for range 2 {
		mock.ExpectQuery("peer_clusters").WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id", "shared_tenant_ids", "cluster_name", "cluster_type", "foghorn_addr", "foghorn_addrs", "control_cell_id",
		}))
	}
	// These snapshots exceed the stream's receive window. The client never reads
	// them, so the second one waits on flow control and draining must release it.
	for _, name := range []string{"EU", "EU changed"} {
		mock.ExpectQuery("peer_clusters").WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id", "shared_tenant_ids", "cluster_name", "cluster_type", "foghorn_addr", "foghorn_addrs", "control_cell_id",
		}).AddRow("eu", []byte("{tenant}"), name, "central", strings.Repeat("x", 2*1024*1024), []byte("{}"), "eu-cell"))
	}
	peerCtx, drainPeers := context.WithCancel(context.Background())
	defer drainPeers()
	hub := newPeerWatchHub()
	hub.ctx = peerCtx
	qm := &QuartermasterServer{db: db, peerWatch: hub}
	entered, release := make(chan struct{}), make(chan struct{})
	var slowSends atomic.Int32
	firstSlowSent := make(chan struct{})
	blockedSendEntered, blockedSendExited := make(chan struct{}), make(chan struct{})
	server := grpc.NewServer(
		grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, servicePeerStream{
				ServerStream: stream, slowSends: &slowSends, firstSlowSent: firstSlowSent,
				blockedSendEntered: blockedSendEntered, blockedSendExited: blockedSendExited,
			})
		}),
		grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
			var req emptypb.Empty
			if recvErr := stream.RecvMsg(&req); recvErr != nil {
				return recvErr
			}
			close(entered)
			select {
			case <-release:
				return stream.SendMsg(&emptypb.Empty{})
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}),
	)
	t.Cleanup(server.Stop)
	quartermasterpb.RegisterClusterServiceServer(server, qm)
	fwserver.RegisterHealthServer(server, health.NewServer())
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { _ = listener.Close() })
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	runDone := make(chan error, 1)
	cleanup := make(chan struct{})
	go func() {
		runDone <- fwserver.Run(runCtx, fwserver.RunSpec{
			Service: "quartermaster", Logger: logging.NewLogger(),
			GRPC:            []fwserver.GRPCListener{{Name: "grpc", Listener: listener, Server: server}},
			ShutdownTimeout: 3 * time.Second,
			OnDrain:         []func(context.Context){func(context.Context) { drainPeers() }},
			OnShutdown:      []func(context.Context){func(context.Context) { close(cleanup) }},
		})
	}()
	// Fixed windows turn off BDP window growth, which would otherwise let the
	// transport flush the unread snapshots and replenish the write quota.
	conn, err := grpc.NewClient("passthrough:///peer-drain", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithInitialWindowSize(65535), grpc.WithInitialConnWindowSize(65535),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := quartermasterpb.NewClusterServiceClient(conn)
	var watches []quartermasterpb.ClusterService_WatchPeersClient
	for range 2 {
		watch, watchErr := client.WatchPeers(ctx, &quartermasterpb.ListPeersRequest{ClusterId: "us"})
		if watchErr != nil {
			t.Fatal(watchErr)
		}
		if _, recvErr := watch.Recv(); recvErr != nil {
			t.Fatal(recvErr)
		}
		watches = append(watches, watch)
	}
	slowCtx, closeSlowReader := context.WithCancel(ctx)
	defer closeSlowReader()
	if _, err = client.WatchPeers(slowCtx, &quartermasterpb.ListPeersRequest{ClusterId: "slow"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstSlowSent:
	case <-ctx.Done():
		t.Fatal("slow-reader snapshot never sent")
	}
	// Only the slow reader, the last subscriber, is woken: the other watchers'
	// re-reads would take the changed snapshot.
	hub.mu.Lock()
	slowWake := hub.waiters[hub.next]
	hub.mu.Unlock()
	slowWake <- struct{}{}
	select {
	case <-blockedSendEntered:
	case <-ctx.Done():
		t.Fatal("slow-reader changed snapshot never started")
	}
	select {
	case <-blockedSendExited:
		t.Fatal("the changed snapshot was sent to a reader that consumed nothing; flow control did not hold it")
	case <-time.After(200 * time.Millisecond):
	}
	healthWatch, err := grpc_health_v1.NewHealthClient(conn).Watch(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := healthWatch.Recv(); err != nil {
		t.Fatal(err)
	}
	rpcDone := make(chan error, 1)
	go func() { rpcDone <- conn.Invoke(ctx, "/test.Business/Finish", &emptypb.Empty{}, &emptypb.Empty{}) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("business RPC never entered")
	}
	stopRun()
	for _, watch := range watches {
		if _, err := watch.Recv(); status.Code(err) != codes.Unavailable {
			t.Fatalf("drained peer watch: %v", err)
		}
	}
	// The blocked Send is released only when the handler's return finishes the
	// stream, after serveHubWatch has unsubscribed, so the hub is empty from here.
	select {
	case <-blockedSendExited:
	case <-ctx.Done():
		t.Fatal("drain left the flow-controlled send blocked")
	}
	// Handler return ends Send but cannot force the client to consume queued
	// bytes. Reset this deliberately unread stream after proving Send ended;
	// the runner's bounded forced-stop path covers clients refusing both.
	closeSlowReader()
	if hub.watcherCount() != 0 {
		t.Fatal("drain left peer subscribers registered")
	}
	if _, _, err := hub.subscribe(); status.Code(err) != codes.Unavailable {
		t.Fatalf("draining hub accepted a new subscriber: %v", err)
	}
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before the business RPC finished: %v", err)
	default:
	}
	close(release)
	if err := <-rpcDone; err != nil {
		t.Fatalf("business RPC interrupted: %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("graceful shutdown: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-cleanup:
	default:
		t.Fatal("shutdown skipped cleanup")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type blockedPeerStream struct {
	grpc.ServerStream
	ctx     context.Context
	entered chan struct{}
	exited  chan struct{}
}

func (s *blockedPeerStream) Context() context.Context { return s.ctx }

func (s *blockedPeerStream) Send(*quartermasterpb.ListPeersResponse) error {
	close(s.entered)
	defer close(s.exited)
	<-s.ctx.Done()
	return s.ctx.Err()
}

func TestWatchPeersDrainReleasesBlockedSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery("peer_clusters").WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id", "shared_tenant_ids", "cluster_name", "cluster_type", "foghorn_addr", "foghorn_addrs", "control_cell_id",
		}))
		peerCtx, drain := context.WithCancel(context.Background())
		defer drain()
		hub := newPeerWatchHub()
		hub.ctx = peerCtx
		qm := &QuartermasterServer{db: db, peerWatch: hub}
		streamCtx, closeTransport := context.WithCancel(serviceCallerContext())
		defer closeTransport()
		stream := &blockedPeerStream{ctx: streamCtx, entered: make(chan struct{}), exited: make(chan struct{})}
		done := make(chan error, 1)
		go func() { done <- qm.WatchPeers(&quartermasterpb.ListPeersRequest{ClusterId: "us"}, stream) }()
		<-stream.entered
		drain()
		synctest.Wait()
		select {
		case err := <-done:
			if status.Code(err) != codes.Unavailable {
				t.Fatalf("drained blocked sender: %v", err)
			}
		default:
			t.Fatal("blocked Send prevented handler return")
		}
		// gRPC cancels the transport context after the handler returns.
		closeTransport()
		synctest.Wait()
		select {
		case <-stream.exited:
		default:
			t.Fatal("blocked Send survived transport closure")
		}
		if hub.watcherCount() != 0 {
			t.Fatal("blocked sender retained its hub slot")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWatchPeersDrainCancelsPendingQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery("peer_clusters").WillDelayFor(time.Hour).WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id", "shared_tenant_ids", "cluster_name", "cluster_type", "foghorn_addr", "foghorn_addrs", "control_cell_id",
		}))
		peerCtx, drain := context.WithCancel(context.Background())
		defer drain()
		hub := newPeerWatchHub()
		hub.ctx = peerCtx
		qm := &QuartermasterServer{db: db, peerWatch: hub}
		stream := &recordingPeerStream{ctx: serviceCallerContext()}
		done := make(chan error, 1)
		go func() { done <- qm.WatchPeers(&quartermasterpb.ListPeersRequest{ClusterId: "us"}, stream) }()
		synctest.Wait()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("query did not start: %v", err)
		}
		start := time.Now()
		drain()
		synctest.Wait()
		if err := <-done; status.Code(err) != codes.Unavailable {
			t.Fatalf("drained query: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("drain waited for the query delay: %s", elapsed)
		}
		if db.Stats().InUse != 0 || stream.count() != 0 {
			t.Fatal("drain left a query running or sent a stale snapshot")
		}
	})
}
