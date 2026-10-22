package grpcutil

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
)

// ReconnectBackoff paces a client's redials of a peer that refuses or drops
// its connection. grpc.ConnectParams replaces gRPC's default backoff with its
// own Backoff field, and a zero Backoff redials without any delay: a client of
// a stopped service then opens thousands of connections per second, and a
// same-host dial that connects to itself holds the service's port in TIME_WAIT
// so the restarted service cannot bind it. Our peers are internal services
// that come back within seconds of a restart, so the delay caps at 15s instead
// of gRPC's default 120s: a recovered peer is redialed within 15s, and a dead
// one sees at most one dial per subchannel every 15s.
var ReconnectBackoff = backoff.Config{
	BaseDelay:  time.Second,
	Multiplier: 1.6,
	Jitter:     0.2,
	MaxDelay:   15 * time.Second,
}

// WithConnectParams bounds each connection attempt by minConnectTimeout and
// paces redials with ReconnectBackoff. Every FrameWorks client sets its connect
// timeout through it; a bare grpc.ConnectParams literal would drop the backoff.
func WithConnectParams(minConnectTimeout time.Duration) grpc.DialOption {
	return grpc.WithConnectParams(grpc.ConnectParams{
		Backoff:           ReconnectBackoff,
		MinConnectTimeout: minConnectTimeout,
	})
}
