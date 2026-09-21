package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
)

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return lis
}

func dialGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestRunDrainsInFlightGRPCCallBeforeReturning(t *testing.T) {
	resetReloadFnsForTest(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	grpcServer := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		var in emptypb.Empty
		if err := stream.RecvMsg(&in); err != nil {
			return err
		}
		close(entered)
		<-release
		return stream.SendMsg(&emptypb.Empty{})
	}))
	grpcLis := listenLocal(t)
	RegisterHealthServer(grpcServer, health.NewServer())
	ready := monitoring.NewReadinessChecker("svc", "v1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, RunSpec{
			Service:         "svc",
			Logger:          logging.NewLogger(),
			Ready:           ready,
			GRPC:            []GRPCListener{{Name: "grpc", Listener: grpcLis, Server: grpcServer}},
			ShutdownTimeout: 5 * time.Second,
		})
	}()

	conn := dialGRPC(t, grpcLis.Addr().String())
	watch, err := grpc_health_v1.NewHealthClient(conn).Watch(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := watch.Recv(); err != nil {
		t.Fatal(err)
	}
	callErr := make(chan error, 1)
	go func() {
		callErr <- conn.Invoke(context.Background(), "/test.Slow/Call", &emptypb.Empty{}, &emptypb.Empty{})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("RPC handler never started")
	}

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for !ready.ShuttingDown() {
		if time.Now().After(deadline) {
			t.Fatal("readiness never switched to draining")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-runErr:
		t.Fatalf("Run returned while an RPC was still in flight: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	if err := <-callErr; err != nil {
		t.Fatalf("in-flight RPC was cut off during shutdown: %v", err)
	}
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A long-lived stream holds GracefulStop open; an OnDrain hook that ends it lets
// the server drain cleanly well inside the shutdown timeout.
func TestRunOnDrainEndsLongLivedStreamsBeforeListenersStop(t *testing.T) {
	resetReloadFnsForTest(t)
	entered := make(chan struct{})
	endStreams := make(chan struct{})
	grpcServer := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		var in emptypb.Empty
		if err := stream.RecvMsg(&in); err != nil {
			return err
		}
		close(entered)
		<-endStreams
		return stream.SendMsg(&emptypb.Empty{})
	}))
	grpcLis := listenLocal(t)
	ready := monitoring.NewReadinessChecker("svc", "v1")
	drainedWhileReady := make(chan bool, 1)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, RunSpec{
			Service:         "svc",
			Logger:          logging.NewLogger(),
			Ready:           ready,
			GRPC:            []GRPCListener{{Name: "grpc", Listener: grpcLis, Server: grpcServer}},
			ShutdownTimeout: 2 * time.Second,
			OnDrain: []func(context.Context){func(context.Context) {
				drainedWhileReady <- !ready.ShuttingDown()
				close(endStreams)
			}},
		})
	}()

	conn := dialGRPC(t, grpcLis.Addr().String())
	callErr := make(chan error, 1)
	go func() {
		callErr <- conn.Invoke(context.Background(), "/test.Stream/Hold", &emptypb.Empty{}, &emptypb.Empty{})
	}()
	<-entered
	start := time.Now()
	cancel()

	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain took %s; the OnDrain hook should have ended the stream immediately", elapsed)
	}
	if notDraining := <-drainedWhileReady; notDraining {
		t.Fatal("OnDrain ran before readiness reported draining")
	}
	if err := <-callErr; err != nil {
		t.Fatalf("stream ended by OnDrain should complete normally: %v", err)
	}
}

func TestRunForcesGRPCStopAfterShutdownTimeout(t *testing.T) {
	resetReloadFnsForTest(t)
	entered := make(chan struct{})
	cleanupErr := make(chan error, 1)
	grpcServer := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		var in emptypb.Empty
		if err := stream.RecvMsg(&in); err != nil {
			return err
		}
		close(entered)
		<-stream.Context().Done()
		return stream.Context().Err()
	}))
	grpcLis := listenLocal(t)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, RunSpec{
			Service:         "svc",
			Logger:          logging.NewLogger(),
			GRPC:            []GRPCListener{{Name: "grpc", Listener: grpcLis, Server: grpcServer}},
			ShutdownTimeout: 200 * time.Millisecond,
			OnShutdown:      []func(context.Context){func(ctx context.Context) { cleanupErr <- ctx.Err() }},
		})
	}()
	conn := dialGRPC(t, grpcLis.Addr().String())
	go func() {
		_ = conn.Invoke(context.Background(), "/test.Hang/Call", &emptypb.Empty{}, &emptypb.Empty{})
	}()
	<-entered
	cancel()

	select {
	case err := <-runErr:
		if err == nil || !strings.Contains(err.Error(), "did not drain") {
			t.Fatalf("expected a drain timeout error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not force the gRPC server closed after the shutdown timeout")
	}
	if err := <-cleanupErr; err != nil {
		t.Fatalf("cleanup inherited expired drain context: %v", err)
	}
}

func TestRunServesAndStopsMultipleListeners(t *testing.T) {
	resetReloadFnsForTest(t)
	httpA, httpB := listenLocal(t), listenLocal(t)
	grpcA, grpcB := listenLocal(t), listenLocal(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, RunSpec{
			Service: "svc",
			Logger:  logging.NewLogger(),
			HTTP: []HTTPListener{
				{Name: "public", Listener: httpA, Handler: handler},
				{Name: "internal", Listener: httpB, Handler: handler},
			},
			GRPC: []GRPCListener{
				{Name: "internal", Listener: grpcA, Server: grpc.NewServer()},
				{Name: "external", Listener: grpcB, Server: grpc.NewServer()},
			},
		})
	}()

	for _, lis := range []net.Listener{httpA, httpB} {
		url := "http://" + lis.Addr().String() + "/"
		var resp *http.Response
		var err error
		for range 50 {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
			if resp, err = http.DefaultClient.Do(req); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("GET %s = %d", url, resp.StatusCode)
		}
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	for _, lis := range []net.Listener{httpA, httpB, grpcA, grpcB} {
		var d net.Dialer
		if conn, err := d.DialContext(context.Background(), "tcp", lis.Addr().String()); err == nil {
			_ = conn.Close()
			t.Fatalf("listener %s still accepts connections after shutdown", lis.Addr())
		}
	}
}

// Server construction can wait minutes for TLS files; HTTP health must serve
// meanwhile, as it did when services built their gRPC server in a goroutine.
func TestRunServesHTTPWhileGRPCBuildWaits(t *testing.T) {
	resetReloadFnsForTest(t)
	httpLis, grpcLis := listenLocal(t), listenLocal(t)
	release := make(chan struct{})
	ready := monitoring.NewReadinessChecker("svc", "v1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, RunSpec{
			Service: "svc",
			Logger:  logging.NewLogger(),
			Ready:   ready,
			HTTP: []HTTPListener{{Name: "http", Listener: httpLis, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})}},
			GRPC: []GRPCListener{{Name: "grpc", Listener: grpcLis, Build: func(ctx context.Context) (*grpc.Server, error) {
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
					var in emptypb.Empty
					if err := stream.RecvMsg(&in); err != nil {
						return err
					}
					return stream.SendMsg(&emptypb.Empty{})
				})), nil
			}}},
		})
	}()

	url := "http://" + httpLis.Addr().String() + "/"
	var lastErr error
	served := false
	for range 100 {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			served = resp.StatusCode == http.StatusOK
			break
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	if !served {
		t.Fatalf("HTTP did not serve while the gRPC build was pending: %v", lastErr)
	}
	if status := ready.Check(); status.Status != monitoring.StatusUnhealthy || status.Checks["grpc_grpc"].Status != monitoring.StatusUnhealthy {
		t.Fatalf("readiness before the gRPC server serves = %+v, want unhealthy grpc_grpc", status)
	}

	close(release)
	conn := dialGRPC(t, grpcLis.Addr().String())
	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	if err := conn.Invoke(callCtx, "/test.Echo/Call", &emptypb.Empty{}, &emptypb.Empty{}, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("gRPC call after build: %v", err)
	}
	if status := ready.Check(); status.Checks["grpc_grpc"].Status != monitoring.StatusHealthy {
		t.Fatalf("readiness after the gRPC server serves = %+v", status)
	}

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunCancelsPendingGRPCBuildOnShutdown(t *testing.T) {
	resetReloadFnsForTest(t)
	grpcLis := listenLocal(t)
	buildReturned := make(chan error, 1)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, RunSpec{
			Service: "svc",
			Logger:  logging.NewLogger(),
			GRPC: []GRPCListener{{Name: "grpc", Listener: grpcLis, Build: func(ctx context.Context) (*grpc.Server, error) {
				<-ctx.Done()
				buildReturned <- ctx.Err()
				return nil, ctx.Err()
			}}},
		})
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return while a gRPC build was pending")
	}
	select {
	case <-buildReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("pending build was not cancelled")
	}
	var d net.Dialer
	if conn, err := d.DialContext(context.Background(), "tcp", grpcLis.Addr().String()); err == nil {
		_ = conn.Close()
		t.Fatal("gRPC port still accepts connections after shutdown")
	}
}

func TestRunStopsWhenGRPCBuildFails(t *testing.T) {
	resetReloadFnsForTest(t)
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), RunSpec{
			Service: "svc",
			Logger:  logging.NewLogger(),
			GRPC: []GRPCListener{{Name: "grpc", Listener: listenLocal(t), Build: func(context.Context) (*grpc.Server, error) {
				return nil, errors.New("tls files never appeared")
			}}},
		})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "tls files never appeared") {
			t.Fatalf("expected the build error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept serving after the gRPC build failed")
	}
}

func TestRunRejectsGRPCListenerWithoutExactlyOneServerSource(t *testing.T) {
	resetReloadFnsForTest(t)
	build := func(context.Context) (*grpc.Server, error) { return grpc.NewServer(), nil }
	for name, listener := range map[string]GRPCListener{
		"neither": {Name: "grpc"},
		"both":    {Name: "grpc", Server: grpc.NewServer(), Build: build},
	} {
		listener.Listener = listenLocal(t)
		err := Run(context.Background(), RunSpec{Service: "svc", Logger: logging.NewLogger(), GRPC: []GRPCListener{listener}})
		if err == nil || !strings.Contains(err.Error(), "exactly one of Server or Build") {
			t.Fatalf("%s: expected a validation error, got %v", name, err)
		}
	}
}

func TestRunFailsFastWhenAPortIsTaken(t *testing.T) {
	resetReloadFnsForTest(t)
	taken := listenLocal(t)
	defer func() { _ = taken.Close() }()
	_, port, _ := net.SplitHostPort(taken.Addr().String())
	free := listenLocal(t)

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), RunSpec{
			Service: "svc",
			Logger:  logging.NewLogger(),
			GRPC:    []GRPCListener{{Name: "grpc", Listener: free, Server: grpc.NewServer()}},
			HTTP:    []HTTPListener{{Name: "http", BindAddr: "127.0.0.1", Port: port, Handler: http.NotFoundHandler()}},
		})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), `HTTP listener "http"`) {
			t.Fatalf("expected bind error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run served instead of failing on a taken port")
	}
}
