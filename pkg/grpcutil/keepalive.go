package grpcutil

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// KeepaliveMinTime is the shortest ping interval a server built by NewServer
// admits. Every client keepalive in this package pings no more often than
// this, and only while a stream is open, so such a server never answers them
// with a too_many_pings GOAWAY.
const KeepaliveMinTime = 15 * time.Second

// serverKeepalive pings a client after Time without reading from it and drops
// the connection when no ack arrives within Timeout, so streams held by a
// client whose host died are released instead of lingering until TCP gives up.
var serverKeepalive = keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}

// serverKeepaliveEnforcement admits the client keepalives of this package.
// gRPC's default policy (one ping per 5 minutes) answers faster pings with a
// too_many_pings GOAWAY, which drops every RPC on that connection.
var serverKeepaliveEnforcement = keepalive.EnforcementPolicy{MinTime: KeepaliveMinTime}

// NewServer builds every FrameWorks gRPC server, so all of them share one
// keepalive and one ping enforcement policy that the clients in this package
// are sized against. opts must not set keepalive options of their own.
func NewServer(opts ...grpc.ServerOption) *grpc.Server {
	return newServerWithEnforcement(serverKeepaliveEnforcement, opts...)
}

func newServerWithEnforcement(enforcement keepalive.EnforcementPolicy, opts ...grpc.ServerOption) *grpc.Server {
	return grpc.NewServer(append([]grpc.ServerOption{
		grpc.KeepaliveParams(serverKeepalive),
		grpc.KeepaliveEnforcementPolicy(enforcement),
	}, opts...)...)
}

// ReplicaClientKeepalive pings every connection that carries a stream, which
// includes the health Watch that ReplicaServiceConfig keeps open on each
// replica. A replica whose host dies without closing its sockets is then torn
// down within Time+Timeout instead of holding the RPCs balanced onto it until
// TCP gives up. Servers built by NewServer accept it.
func ReplicaClientKeepalive() grpc.DialOption {
	return grpc.WithKeepaliveParams(replicaClientKeepalive)
}

var replicaClientKeepalive = keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}

// ControlStreamKeepalive is the keepalive of a client whose work rides one
// long-lived stream that must move to another server instance when its
// server stops answering: Helmsman's control stream to Foghorn. Writes into a
// dead peer's socket keep succeeding into the kernel buffer and a read waits
// indefinitely, so without pings a host that died without closing its sockets
// holds the stream until TCP retransmission gives up, many minutes later. The
// client pings after Time without reading anything and closes the connection
// when no ack arrives within Timeout, failing the stream within half a minute
// of the last byte read.
func ControlStreamKeepalive() keepalive.ClientParameters {
	return keepalive.ClientParameters{Time: 20 * time.Second, Timeout: 10 * time.Second}
}
