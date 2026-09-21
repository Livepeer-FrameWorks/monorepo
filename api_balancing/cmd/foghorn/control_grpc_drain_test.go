package main

import (
	"context"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
)

func loopbackListener(t *testing.T) net.Listener {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return lis
}

// serveForDrainTest serves srv on a loopback port and returns the address and a
// channel closed once Serve returns.
func serveForDrainTest(t *testing.T, srv *grpc.Server) (string, <-chan struct{}) {
	t.Helper()
	lis := loopbackListener(t)
	stopped := make(chan struct{})
	go func() {
		_ = srv.Serve(lis)
		close(stopped)
	}()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), stopped
}

func healthServer() *grpc.Server {
	srv := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(srv, health.NewServer())
	return srv
}

// answers reports whether a gRPC health check against addr succeeds.
func answers(t *testing.T, addr string) bool {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	return err == nil
}

// The drain is wired into server.Run the way main wires it: Build wrappers
// record the servers and OnDrain runs the cleanup. The cleanup must see both
// control servers still answering, and Run must return with both stopped.
func TestControlGRPCDrainRunsFromRunOnDrainWhileServersServe(t *testing.T) {
	internalLis, externalLis := loopbackListener(t), loopbackListener(t)
	internalAddr, externalAddr := internalLis.Addr().String(), externalLis.Addr().String()

	var cleanups atomic.Int32
	var cleanupSawServing atomic.Bool
	drain := newControlGRPCDrain(5*time.Second, func(context.Context) {
		cleanups.Add(1)
		cleanupSawServing.Store(answers(t, internalAddr) && answers(t, externalAddr))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- server.Run(ctx, server.RunSpec{
			Service: "foghorn-drain-test",
			Logger:  logging.NewLogger(),
			GRPC: []server.GRPCListener{
				{Name: "internal", Listener: internalLis, Build: drain.internalBuild(func(context.Context) (*grpc.Server, error) { return healthServer(), nil })},
				{Name: "external", Listener: externalLis, Build: drain.externalBuild(func(context.Context) (*grpc.Server, error) { return healthServer(), nil })},
			},
			OnDrain:         []func(context.Context){drain.onDrain},
			ShutdownTimeout: 10 * time.Second,
		})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !answers(t, internalAddr) || !answers(t, externalAddr) {
		if time.Now().After(deadline) {
			t.Fatal("control gRPC servers never started serving")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("cleanup ran %d times, want exactly once", got)
	}
	if !cleanupSawServing.Load() {
		t.Fatal("cleanup ran after a control gRPC server had stopped")
	}
	if answers(t, internalAddr) || answers(t, externalAddr) {
		t.Fatal("a control gRPC server still answers after Run returned")
	}
}

func TestControlGRPCDrainStopsOpenStreamsWithinBudget(t *testing.T) {
	srv := healthServer()
	addr, stopped := serveForDrainTest(t, srv)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	watch, err := grpc_health_v1.NewHealthClient(conn).Watch(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := watch.Recv(); err != nil {
		t.Fatalf("open health watch stream: %v", err)
	}

	drain := newControlGRPCDrain(200*time.Millisecond, func(context.Context) {})
	if _, err := drain.externalBuild(func(context.Context) (*grpc.Server, error) { return srv, nil })(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	drain.run()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("an open stream kept the server running past the drain budget")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("drain took %s with a 200ms budget", elapsed)
	}
}

func TestControlGRPCDrainWithoutBuiltServers(t *testing.T) {
	var cleanups atomic.Int32
	drain := newControlGRPCDrain(time.Second, func(context.Context) { cleanups.Add(1) })
	drain.run()
	drain.run()
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("cleanup ran %d times, want exactly once", got)
	}
}

func TestControlGRPCDrainHandsOffBeforeCleanupAndEndsHealthWatches(t *testing.T) {
	srv := grpc.NewServer()
	server.RegisterHealthServer(srv, health.NewServer())
	addr, _ := serveForDrainTest(t, srv)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watch, err := grpc_health_v1.NewHealthClient(conn).Watch(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, recvErr := watch.Recv(); recvErr != nil {
		t.Fatal(recvErr)
	}
	var steps []string
	drain := newControlGRPCDrain(3*time.Second, func(context.Context) { steps = append(steps, "cleanup") })
	drain.begin = func(_ context.Context, closeListeners func()) {
		steps = append(steps, "refuse-new-streams")
		closeListeners()
		for {
			if _, recvErr := watch.Recv(); recvErr != nil {
				break
			}
		}
		if ctx.Err() != nil {
			t.Fatal("health Watch survived listener drain")
		}
		steps = append(steps, "handoff")
	}
	if _, buildErr := drain.externalBuild(func(context.Context) (*grpc.Server, error) { return srv, nil })(ctx); buildErr != nil {
		t.Fatal(buildErr)
	}
	drain.run()
	if !slices.Equal(steps, []string{"refuse-new-streams", "handoff", "cleanup"}) {
		t.Fatalf("shutdown order = %v", steps)
	}
}
