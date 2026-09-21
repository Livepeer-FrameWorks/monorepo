package server

import (
	"context"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

var grpcHealthServers sync.Map // *grpc.Server -> *drainingHealthServer

type drainingHealthServer struct {
	*health.Server
	ctx    context.Context
	cancel context.CancelFunc
}

// RegisterHealthServer registers health checks whose permanent Watch streams end
// when Run drains this listener. Business RPCs keep their full shutdown budget.
func RegisterHealthServer(srv *grpc.Server, hs *health.Server) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &drainingHealthServer{Server: hs, ctx: ctx, cancel: cancel}
	grpc_health_v1.RegisterHealthServer(srv, h)
	grpcHealthServers.Store(srv, h)
}

type healthWatchStream struct {
	grpc_health_v1.Health_WatchServer
	ctx context.Context
}

func (s healthWatchStream) Context() context.Context { return s.ctx }

func (h *drainingHealthServer) Watch(req *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel)
	defer stop()
	return h.Server.Watch(req, healthWatchStream{Health_WatchServer: stream, ctx: ctx})
}

// DrainGRPCHealth ends permanent health watches before a custom listener drain.
// Run also calls it for listeners it stops directly; repeated calls are safe.
func DrainGRPCHealth(srv *grpc.Server) {
	if entry, ok := grpcHealthServers.LoadAndDelete(srv); ok {
		if h, valid := entry.(*drainingHealthServer); valid {
			h.Shutdown()
			h.cancel()
		}
	}
}
