package main

import (
	"context"
	"sync"
	"time"

	fwserver "github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"google.golang.org/grpc"
)

// controlGRPCDrain stops the internal and external control gRPC servers that
// server.Run builds in the background. It runs as a RunSpec.OnDrain hook, which
// Run calls after readiness flips to draining and before any listener stops,
// so the optional begin hook can refuse new streams, close listeners and notify
// connected nodes before cleanup stops the trigger processor. Both listeners
// drain concurrently within one budget, with Stop as the bounded fallback.
type controlGRPCDrain struct {
	budget  time.Duration
	cleanup func(ctx context.Context)
	begin   func(ctx context.Context, closeListeners func())

	mu       sync.Mutex
	internal *grpc.Server
	external *grpc.Server

	runOnce sync.Once
}

func newControlGRPCDrain(budget time.Duration, cleanup func(ctx context.Context)) *controlGRPCDrain {
	return &controlGRPCDrain{budget: budget, cleanup: cleanup}
}

// internalBuild wraps construct as a server.GRPCListener Build for the
// internal server.
func (d *controlGRPCDrain) internalBuild(construct func(context.Context) (*grpc.Server, error)) func(context.Context) (*grpc.Server, error) {
	return d.build(construct, func(srv *grpc.Server) { d.internal = srv })
}

// externalBuild wraps construct as a server.GRPCListener Build for the
// external server.
func (d *controlGRPCDrain) externalBuild(construct func(context.Context) (*grpc.Server, error)) func(context.Context) (*grpc.Server, error) {
	return d.build(construct, func(srv *grpc.Server) { d.external = srv })
}

func (d *controlGRPCDrain) build(construct func(context.Context) (*grpc.Server, error), record func(*grpc.Server)) func(context.Context) (*grpc.Server, error) {
	return func(ctx context.Context) (*grpc.Server, error) {
		srv, err := construct(ctx)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		record(srv)
		d.mu.Unlock()
		return srv, nil
	}
}

// onDrain is the RunSpec.OnDrain hook.
func (d *controlGRPCDrain) onDrain(context.Context) { d.run() }

// run performs the drain once; a later call blocks until it has finished.
// Servers that were never built are skipped.
func (d *controlGRPCDrain) run() {
	d.runOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), d.budget)
		defer cancel()
		d.mu.Lock()
		servers := make([]*grpc.Server, 0, 2)
		for _, srv := range []*grpc.Server{d.internal, d.external} {
			if srv != nil {
				servers = append(servers, srv)
			}
		}
		d.mu.Unlock()

		done := make(chan struct{})
		var stopping sync.Once
		startStops := func() {
			stopping.Do(func() {
				var wg sync.WaitGroup
				for _, srv := range servers {
					fwserver.DrainGRPCHealth(srv)
					wg.Add(1)
					go func() { defer wg.Done(); srv.GracefulStop() }()
				}
				go func() { wg.Wait(); close(done) }()
			})
		}
		if d.begin != nil {
			d.begin(ctx, startStops)
		}
		d.cleanup(ctx)
		startStops()
		select {
		case <-done:
		case <-ctx.Done():
			for _, srv := range servers {
				srv.Stop()
			}
			<-done
		}
	})
}
